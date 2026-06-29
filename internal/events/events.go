package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"radio_engine/internal/config"
)

type Event struct {
	EventID       string         `json:"eventId"`
	Type          string         `json:"type"`
	StationID     string         `json:"stationId,omitempty"`
	InstanceID    string         `json:"instanceId"`
	Sequence      uint64         `json:"sequence,omitempty"`
	Timestamp     time.Time      `json:"timestamp"`
	CorrelationID string         `json:"correlationId,omitempty"`
	Payload       map[string]any `json:"payload,omitempty"`
}

type Publisher interface {
	Publish(Event) error
	Ping(context.Context) error
	Close(context.Context) error
	Backlog() int
}

var ErrQueueFull = errors.New("event queue is full")

type Dispatcher struct {
	client     *redis.Client
	stream     string
	instanceID string
	timeout    time.Duration
	queue      chan Event
	done       chan struct{}
	cancel     context.CancelFunc
	closed     atomic.Bool
	closeOnce  sync.Once
}

func NewDispatcher(cfg config.Redis, instanceID string, capacity int) *Dispatcher {
	if capacity <= 0 {
		capacity = 4096
	}
	if cfg.Stream == "" {
		cfg.Stream = "radio.events"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{
		client: redis.NewClient(&redis.Options{
			Addr:         cfg.Address,
			Password:     cfg.Password,
			DB:           cfg.DB,
			DialTimeout:  cfg.Timeout,
			ReadTimeout:  cfg.Timeout,
			WriteTimeout: cfg.Timeout,
		}),
		stream:     cfg.Stream,
		instanceID: instanceID,
		timeout:    cfg.Timeout,
		queue:      make(chan Event, capacity),
		done:       make(chan struct{}),
		cancel:     cancel,
	}
	go d.run(ctx)
	return d
}

func (d *Dispatcher) Publish(event Event) error {
	if d.closed.Load() {
		return errors.New("event dispatcher is closed")
	}
	if event.EventID == "" {
		event.EventID = uuid.NewString()
	}
	if event.InstanceID == "" {
		event.InstanceID = d.instanceID
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	select {
	case d.queue <- event:
		return nil
	default:
		return ErrQueueFull
	}
}

func (d *Dispatcher) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.client.Ping(ctx).Err()
}

func (d *Dispatcher) Backlog() int { return len(d.queue) }

func (d *Dispatcher) Close(ctx context.Context) error {
	d.closeOnce.Do(func() {
		d.closed.Store(true)
		close(d.queue)
	})
	select {
	case <-d.done:
		d.cancel()
		return d.client.Close()
	case <-ctx.Done():
		d.cancel()
		return ctx.Err()
	}
}

func (d *Dispatcher) run(ctx context.Context) {
	defer close(d.done)
	for event := range d.queue {
		backoff := 100 * time.Millisecond
		for {
			if err := d.append(ctx, event); err == nil {
				break
			} else {
				slog.Error("publish redis event", "event_id", event.EventID, "type", event.Type, "error", err)
			}
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
		}
	}
}

func (d *Dispatcher) append(ctx context.Context, event Event) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	writeCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.client.XAdd(writeCtx, &redis.XAddArgs{
		Stream: d.stream,
		Values: map[string]any{
			"eventId":       event.EventID,
			"type":          event.Type,
			"stationId":     event.StationID,
			"instanceId":    event.InstanceID,
			"sequence":      event.Sequence,
			"timestamp":     event.Timestamp.Format(time.RFC3339Nano),
			"correlationId": event.CorrelationID,
			"payload":       string(payload),
		},
	}).Err()
}

type MemoryPublisher struct {
	mu     sync.Mutex
	Events []Event
}

func (p *MemoryPublisher) Publish(event Event) error {
	if event.EventID == "" {
		event.EventID = uuid.NewString()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	p.mu.Lock()
	p.Events = append(p.Events, event)
	p.mu.Unlock()
	return nil
}

func (p *MemoryPublisher) Ping(context.Context) error  { return nil }
func (p *MemoryPublisher) Close(context.Context) error { return nil }
func (p *MemoryPublisher) Backlog() int                { return 0 }

func (p *MemoryPublisher) Snapshot() []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]Event, len(p.Events))
	copy(result, p.Events)
	return result
}
