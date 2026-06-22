package station

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"radio_engine/internal/audio"
	"radio_engine/internal/events"
	"radio_engine/internal/media"
	"radio_engine/internal/observability"
	"radio_engine/internal/playlist"
)

const MaxQueueTracks = playlist.MaxQueueTracks

const (
	ModeIdle    = "idle"
	ModePlaying = "playing"
	ModeStopped = "stopped"
)

var (
	ErrQueueTooLarge     = errors.New("station queue is too large")
	ErrStaleRevision     = errors.New("queue revision is stale")
	ErrCommandQueueFull  = errors.New("station command queue is full")
	ErrAnnouncementQueue = errors.New("announcement queue is full")
)

type Source interface {
	MountURL() string
	Connect(context.Context) (io.WriteCloser, error)
}

type Config struct {
	ID                  string                    `json:"id"`
	Name                string                    `json:"name"`
	Description         string                    `json:"description,omitempty"`
	Genre               string                    `json:"genre,omitempty"`
	Public              bool                      `json:"public"`
	Mount               string                    `json:"mount"`
	StreamURL           string                    `json:"streamUrl"`
	Output              audio.OutputConfig        `json:"output"`
	TransitionDefaults  playlist.Transition       `json:"transitionDefaults"`
	AnnouncementProfile audio.AnnouncementProfile `json:"announcementProfile"`
}

func (c Config) Normalize() Config {
	c.Output = c.Output.Normalize()
	c.TransitionDefaults = c.TransitionDefaults.Normalize(playlist.DefaultTransition())
	c.AnnouncementProfile = c.AnnouncementProfile.Normalize()
	return c
}

type QueueState struct {
	Revision uint64           `json:"revision"`
	Tracks   []playlist.Track `json:"tracks"`
}

type State struct {
	Config               Config                 `json:"config"`
	Mode                 string                 `json:"mode"`
	CurrentTrack         *playlist.Track        `json:"currentTrack"`
	Queue                QueueState             `json:"queue"`
	CurrentAnnouncement  *playlist.Announcement `json:"currentAnnouncement"`
	PendingAnnouncements int                    `json:"pendingAnnouncements"`
	StartedAt            time.Time              `json:"startedAt,omitempty"`
	LastError            string                 `json:"lastError,omitempty"`
	Connected            bool                   `json:"connected"`
}

type Dependencies struct {
	Source    Source
	Media     media.Store
	FFmpeg    *audio.FFmpeg
	Events    events.Publisher
	Metrics   *observability.Metrics
	Reconnect time.Duration
}

type Station struct {
	ID   string
	Name string

	deps Dependencies

	mu            sync.Mutex
	config        Config
	mode          string
	current       *playlist.Track
	queue         []playlist.Track
	revision      uint64
	startedAt     time.Time
	lastError     string
	connected     bool
	announcements []playlist.Announcement
	currentAnn    *playlist.Announcement
	seenCommands  map[string]struct{}
	sequence      uint64

	commands chan Command
	wakeup   chan struct{}
}

func New(cfg Config, deps Dependencies) (*Station, error) {
	if cfg.ID == "" {
		return nil, errors.New("station id is required")
	}
	if cfg.Name == "" {
		cfg.Name = cfg.ID
	}
	if deps.Source == nil {
		return nil, errors.New("station source is required")
	}
	if deps.Media == nil {
		return nil, errors.New("media store is required")
	}
	if deps.FFmpeg == nil {
		return nil, errors.New("ffmpeg service is required")
	}
	if deps.Events == nil {
		deps.Events = &events.MemoryPublisher{}
	}
	if deps.Reconnect <= 0 {
		deps.Reconnect = 3 * time.Second
	}
	cfg.StreamURL = deps.Source.MountURL()
	cfg = cfg.Normalize()
	return &Station{
		ID:           cfg.ID,
		Name:         cfg.Name,
		deps:         deps,
		config:       cfg,
		mode:         ModeIdle,
		seenCommands: make(map[string]struct{}),
		commands:     make(chan Command, 32),
		wakeup:       make(chan struct{}, 1),
	}, nil
}

func (s *Station) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := State{
		Config:               s.config,
		Mode:                 s.mode,
		Queue:                QueueState{Revision: s.revision, Tracks: copyTracks(s.queue)},
		PendingAnnouncements: len(s.announcements),
		StartedAt:            s.startedAt,
		LastError:            s.lastError,
		Connected:            s.connected,
	}
	if s.current != nil {
		current := *s.current
		state.CurrentTrack = &current
	}
	if s.currentAnn != nil {
		current := *s.currentAnn
		state.CurrentAnnouncement = &current
	}
	return state
}

func (s *Station) UpdateConfig(cfg Config, correlationID string) {
	cfg.ID = s.ID
	cfg.StreamURL = s.deps.Source.MountURL()
	cfg = cfg.Normalize()
	s.mu.Lock()
	s.config = cfg
	s.Name = cfg.Name
	s.mu.Unlock()
	s.emit("station.config_updated", correlationID, map[string]any{"config": cfg})
}

func (s *Station) ReplaceQueue(revision uint64, tracks []playlist.Track, correlationID string) (State, error) {
	if len(tracks) > MaxQueueTracks {
		return State{}, ErrQueueTooLarge
	}
	s.mu.Lock()
	if revision <= s.revision {
		s.mu.Unlock()
		return State{}, ErrStaleRevision
	}
	normalized := make([]playlist.Track, len(tracks))
	for i, track := range tracks {
		item, err := track.Normalize(s.config.TransitionDefaults)
		if err != nil {
			s.mu.Unlock()
			return State{}, err
		}
		normalized[i] = item
	}
	s.queue = normalized
	s.revision = revision
	s.mu.Unlock()
	s.signal()
	s.emit("queue.applied", correlationID, map[string]any{"revision": revision, "size": len(tracks)})
	if len(tracks) <= 2 {
		s.emit("queue.low", correlationID, map[string]any{"remaining": len(tracks)})
	}
	return s.State(), nil
}

func (s *Station) AddAnnouncement(announcement playlist.Announcement) (bool, error) {
	if err := announcement.Validate(); err != nil {
		return false, err
	}
	s.mu.Lock()
	if _, exists := s.seenCommands[announcement.CommandID]; exists {
		s.mu.Unlock()
		return false, nil
	}
	if len(s.announcements) >= 32 {
		s.mu.Unlock()
		return false, ErrAnnouncementQueue
	}
	s.seenCommands[announcement.CommandID] = struct{}{}
	s.announcements = append(s.announcements, announcement)
	s.mu.Unlock()
	s.signal()
	s.emit("announcement.queued", announcement.CorrelationID, map[string]any{"commandId": announcement.CommandID})
	return true, nil
}

func (s *Station) Send(command Command) error {
	select {
	case s.commands <- command:
		return nil
	default:
		return ErrCommandQueueFull
	}
}

func (s *Station) setConnected(connected bool) {
	s.mu.Lock()
	s.connected = connected
	s.mu.Unlock()
}

func (s *Station) signal() {
	select {
	case s.wakeup <- struct{}{}:
	default:
	}
}

func (s *Station) emit(eventType, correlationID string, payload map[string]any) {
	s.mu.Lock()
	s.sequence++
	sequence := s.sequence
	s.mu.Unlock()
	_ = s.deps.Events.Publish(events.Event{
		Type:          eventType,
		StationID:     s.ID,
		Sequence:      sequence,
		CorrelationID: correlationID,
		Payload:       payload,
	})
}

func copyTracks(tracks []playlist.Track) []playlist.Track {
	result := make([]playlist.Track, len(tracks))
	copy(result, tracks)
	return result
}
