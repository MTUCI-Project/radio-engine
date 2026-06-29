package radio

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"radio_engine/internal/audio"
	"radio_engine/internal/config"
	"radio_engine/internal/events"
	"radio_engine/internal/icecast"
	"radio_engine/internal/media"
	"radio_engine/internal/observability"
	"radio_engine/internal/playlist"
	"radio_engine/internal/station"
)

type SourceFactory func(icecast.Config) station.Source

type Manager struct {
	cfg       config.Config
	store     media.Store
	ffmpeg    *audio.FFmpeg
	events    events.Publisher
	metrics   *observability.Metrics
	newSource SourceFactory

	mu       sync.RWMutex
	rootCtx  context.Context
	cancel   context.CancelFunc
	stations map[string]*runner
	closed   bool
}

type runner struct {
	station *station.Station
	source  station.Source
	cancel  context.CancelFunc
	done    chan error
}

type UpsertStationRequest struct {
	ID                  string
	Name                string
	Description         string
	Genre               string
	Public              bool
	Mount               string
	Output              audio.OutputConfig
	TransitionDefaults  playlist.Transition
	AnnouncementProfile audio.AnnouncementProfile
	CorrelationID       string
}

type Capacity struct {
	Current int `json:"current"`
	Max     int `json:"max"`
}

type ReadyStatus struct {
	Ready    bool              `json:"ready"`
	Capacity Capacity          `json:"capacity"`
	Checks   map[string]string `json:"checks"`
}

var (
	ErrStationNotFound  = errors.New("station not found")
	ErrCommandQueueFull = errors.New("station command queue is full")
	ErrQueueTooLarge    = station.ErrQueueTooLarge
	ErrStaleRevision    = station.ErrStaleRevision
	ErrCapacityExceeded = errors.New("station capacity exceeded")
	ErrManagerClosed    = errors.New("radio manager is closed")
)

func NewManager(cfg config.Config, store media.Store, ffmpeg *audio.FFmpeg, publisher events.Publisher, metrics *observability.Metrics) (*Manager, error) {
	return NewManagerWithSourceFactory(cfg, store, ffmpeg, publisher, metrics, defaultSourceFactory)
}

func NewManagerWithSourceFactory(cfg config.Config, store media.Store, ffmpeg *audio.FFmpeg, publisher events.Publisher, metrics *observability.Metrics, newSource SourceFactory) (*Manager, error) {
	if store == nil {
		return nil, errors.New("media store is required")
	}
	if ffmpeg == nil {
		return nil, errors.New("ffmpeg service is required")
	}
	if publisher == nil {
		publisher = &events.MemoryPublisher{}
	}
	if newSource == nil {
		newSource = defaultSourceFactory
	}
	if cfg.MaxStations <= 0 {
		cfg.MaxStations = 20
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:       cfg,
		store:     store,
		ffmpeg:    ffmpeg,
		events:    publisher,
		metrics:   metrics,
		newSource: newSource,
		rootCtx:   rootCtx,
		cancel:    cancel,
		stations:  map[string]*runner{},
	}, nil
}

func defaultSourceFactory(cfg icecast.Config) station.Source {
	return icecast.NewSource(cfg)
}

func (m *Manager) UpsertStation(req UpsertStationRequest) (station.State, bool, error) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return station.State{}, false, errors.New("station id is required")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return station.State{}, false, ErrManagerClosed
	}
	if existing := m.stations[id]; existing != nil {
		cfg := m.stationConfig(req, "")
		if updater, ok := existing.source.(interface{ Update(icecast.Config) }); ok {
			updater.Update(m.icecastConfig(cfg))
		}
		cfg.StreamURL = existing.source.MountURL()
		m.mu.Unlock()
		existing.station.UpdateConfig(cfg, req.CorrelationID)
		return existing.station.State(), false, nil
	}
	if len(m.stations) >= m.cfg.MaxStations {
		m.mu.Unlock()
		return station.State{}, false, ErrCapacityExceeded
	}
	cfg := m.stationConfig(req, "")
	source := m.newSource(m.icecastConfig(cfg))
	radioStation, err := station.New(cfg, station.Dependencies{
		Source:    source,
		Media:     m.store,
		FFmpeg:    m.ffmpeg,
		Events:    m.events,
		Metrics:   m.metrics,
		Reconnect: 3 * time.Second,
	})
	if err != nil {
		m.mu.Unlock()
		return station.State{}, false, err
	}
	ctx, cancel := context.WithCancel(m.rootCtx)
	r := &runner{station: radioStation, source: source, cancel: cancel, done: make(chan error, 1)}
	m.stations[id] = r
	if m.metrics != nil {
		m.metrics.Stations.Set(float64(len(m.stations)))
	}
	m.mu.Unlock()

	go func() {
		err := radioStation.Run(ctx)
		if errors.Is(err, context.Canceled) {
			err = nil
		}
		r.done <- err
	}()
	_ = m.events.Publish(events.Event{Type: "station.created", StationID: id, CorrelationID: req.CorrelationID})
	return radioStation.State(), true, nil
}

func (m *Manager) stationConfig(req UpsertStationRequest, streamURL string) station.Config {
	id := strings.TrimSpace(req.ID)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = id
	}
	mount := strings.TrimSpace(req.Mount)
	if mount == "" {
		mount = "/stations/" + id + ".mp3"
	}
	if !strings.HasPrefix(mount, "/") {
		mount = "/" + mount
	}
	cfg := station.Config{
		ID:                  id,
		Name:                name,
		Description:         firstNonEmpty(req.Description, m.cfg.Icecast.Description),
		Genre:               firstNonEmpty(req.Genre, m.cfg.Icecast.Genre),
		Public:              req.Public,
		Mount:               mount,
		StreamURL:           streamURL,
		Output:              req.Output,
		TransitionDefaults:  req.TransitionDefaults,
		AnnouncementProfile: req.AnnouncementProfile,
	}
	return cfg.Normalize()
}

func (m *Manager) icecastConfig(cfg station.Config) icecast.Config {
	icecastCfg := m.cfg.Icecast
	icecastCfg.Mount = cfg.Mount
	icecastCfg.Name = cfg.Name
	icecastCfg.Description = cfg.Description
	icecastCfg.Genre = cfg.Genre
	icecastCfg.Public = cfg.Public
	return icecastCfg
}

func (m *Manager) DeleteStation(id, correlationID string) error {
	r, ok := m.removeRunner(id)
	if !ok {
		return ErrStationNotFound
	}
	r.cancel()
	if err := <-r.done; err != nil {
		return fmt.Errorf("stop station %s: %w", id, err)
	}
	_ = m.events.Publish(events.Event{Type: "station.deleted", StationID: id, CorrelationID: correlationID})
	return nil
}

func (m *Manager) ReplaceQueue(id string, revision uint64, tracks []playlist.Track, correlationID string) (station.State, error) {
	r, ok := m.runner(id)
	if !ok {
		return station.State{}, ErrStationNotFound
	}
	return r.station.ReplaceQueue(revision, tracks, correlationID)
}

func (m *Manager) AddAnnouncement(id string, announcement playlist.Announcement) (bool, error) {
	r, ok := m.runner(id)
	if !ok {
		return false, ErrStationNotFound
	}
	return r.station.AddAnnouncement(announcement)
}

func (m *Manager) Command(id string, command station.Command) error {
	r, ok := m.runner(id)
	if !ok {
		return ErrStationNotFound
	}
	if err := r.station.Send(command); errors.Is(err, station.ErrCommandQueueFull) {
		return ErrCommandQueueFull
	} else {
		return err
	}
}

func (m *Manager) Station(id string) (*station.Station, bool) {
	r, ok := m.runner(id)
	if !ok {
		return nil, false
	}
	return r.station, true
}

func (m *Manager) States() []station.State {
	m.mu.RLock()
	stations := make([]*station.Station, 0, len(m.stations))
	for _, r := range m.stations {
		stations = append(stations, r.station)
	}
	m.mu.RUnlock()
	states := make([]station.State, 0, len(stations))
	for _, radioStation := range stations {
		states = append(states, radioStation.State())
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Config.ID < states[j].Config.ID })
	return states
}

func (m *Manager) Capacity() Capacity {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Capacity{Current: len(m.stations), Max: m.cfg.MaxStations}
}

func (m *Manager) Ready(ctx context.Context) ReadyStatus {
	checks := map[string]string{}
	ready := true
	if err := m.store.Ping(ctx); err != nil {
		checks["minio"] = err.Error()
		ready = false
	} else {
		checks["minio"] = "ok"
	}
	if err := m.events.Ping(ctx); err != nil {
		checks["redis"] = err.Error()
		ready = false
	} else {
		checks["redis"] = "ok"
	}
	capacity := m.Capacity()
	if capacity.Current >= capacity.Max {
		checks["capacity"] = "full"
		ready = false
	} else {
		checks["capacity"] = "ok"
	}
	if m.metrics != nil {
		m.metrics.EventBacklog.Set(float64(m.events.Backlog()))
	}
	return ReadyStatus{Ready: ready, Capacity: capacity, Checks: checks}
}

func (m *Manager) Run(ctx context.Context) error {
	<-ctx.Done()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ctx.Err()
	}
	m.closed = true
	m.mu.Unlock()
	m.cancel()
	m.stopAll()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), m.cfg.ShutdownWait)
	defer cancel()
	if err := m.events.Close(shutdownCtx); err != nil {
		slog.Warn("close event dispatcher", "error", err)
	}
	return ctx.Err()
}

func (m *Manager) runner(id string) (*runner, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.stations[id]
	return r, ok
}

func (m *Manager) removeRunner(id string) (*runner, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.stations[id]
	if !ok {
		return nil, false
	}
	delete(m.stations, id)
	if m.metrics != nil {
		m.metrics.Stations.Set(float64(len(m.stations)))
	}
	return r, true
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	runners := make([]*runner, 0, len(m.stations))
	for id, r := range m.stations {
		runners = append(runners, r)
		delete(m.stations, id)
	}
	if m.metrics != nil {
		m.metrics.Stations.Set(0)
	}
	m.mu.Unlock()

	for _, r := range runners {
		r.cancel()
	}
	for _, r := range runners {
		select {
		case err := <-r.done:
			if err != nil {
				slog.Warn("station stopped with error", "station_id", r.station.ID, "error", err)
			}
		case <-time.After(m.cfg.ShutdownWait):
			slog.Warn("station did not stop before timeout", "station_id", r.station.ID)
		}
	}
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
