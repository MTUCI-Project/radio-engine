package radio

import (
	"context"
	"io"
	"testing"
	"time"

	"radio_engine/internal/audio"
	"radio_engine/internal/config"
	"radio_engine/internal/events"
	"radio_engine/internal/icecast"
	"radio_engine/internal/media"
	"radio_engine/internal/playlist"
	"radio_engine/internal/station"
)

func TestUpsertStationAndCapacity(t *testing.T) {
	manager := newManagerForTest(t, 1)
	defer stopManager(t, manager)

	state, created, err := manager.UpsertStation(UpsertStationRequest{ID: "main", Name: "Main"})
	if err != nil {
		t.Fatalf("upsert station: %v", err)
	}
	if !created || state.Config.ID != "main" || state.Config.Mount != "/stations/main.mp3" {
		t.Fatalf("state=%#v created=%t", state, created)
	}
	_, created, err = manager.UpsertStation(UpsertStationRequest{ID: "main", Name: "Main Updated"})
	if err != nil || created {
		t.Fatalf("idempotent update created=%t err=%v", created, err)
	}
	if _, _, err := manager.UpsertStation(UpsertStationRequest{ID: "second"}); err != ErrCapacityExceeded {
		t.Fatalf("capacity error = %v, want ErrCapacityExceeded", err)
	}
}

func TestReplaceQueueDelegatesRevision(t *testing.T) {
	manager := newManagerForTest(t, 2)
	defer stopManager(t, manager)
	if _, _, err := manager.UpsertStation(UpsertStationRequest{ID: "main"}); err != nil {
		t.Fatalf("upsert station: %v", err)
	}
	tracks := []playlist.Track{{ItemID: "1", Media: playlist.MediaRef{Bucket: "b", ObjectKey: "one.mp3"}}}
	state, err := manager.ReplaceQueue("main", 1, tracks, "req")
	if err != nil {
		t.Fatalf("replace queue: %v", err)
	}
	if state.Queue.Revision != 1 || len(state.Queue.Tracks) != 1 {
		t.Fatalf("queue state = %#v", state.Queue)
	}
	if _, err := manager.ReplaceQueue("missing", 1, tracks, "req"); err != ErrStationNotFound {
		t.Fatalf("missing queue error = %v", err)
	}
}

func newManagerForTest(t *testing.T, capacity int) *Manager {
	t.Helper()
	cfg := config.Config{
		MaxStations:  capacity,
		ShutdownWait: time.Second,
		Icecast: icecast.Config{
			URL:         "http://icecast.test",
			SourceUser:  "source",
			SourcePass:  "hackme",
			Description: "Test",
			Genre:       "Various",
		},
	}
	manager, err := NewManagerWithSourceFactory(cfg, media.NewMemoryStore(), audio.NewFFmpeg("ffmpeg", "", nil), &events.MemoryPublisher{}, nil, func(cfg icecast.Config) station.Source {
		return testSource{mountURL: cfg.URL + cfg.Mount}
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func stopManager(t *testing.T, manager *Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Run(ctx); err != context.Canceled {
		t.Fatalf("manager run after cancel = %v, want context.Canceled", err)
	}
}

type testSource struct{ mountURL string }

func (s testSource) MountURL() string { return s.mountURL }
func (s testSource) Connect(context.Context) (io.WriteCloser, error) {
	return discardWriteCloser{}, nil
}

type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }
