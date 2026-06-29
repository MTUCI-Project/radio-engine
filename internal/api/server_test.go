package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"radio_engine/internal/audio"
	"radio_engine/internal/config"
	"radio_engine/internal/events"
	"radio_engine/internal/icecast"
	"radio_engine/internal/media"
	"radio_engine/internal/radio"
	"radio_engine/internal/station"
)

func TestStationLifecycleAPI(t *testing.T) {
	manager := newTestManager(t, 2)
	defer stopTestManager(t, manager)
	handler := NewServer(":0", manager, "").routes()

	req := httptest.NewRequest(http.MethodPut, "/v1/stations/main", bytes.NewBufferString(`{"name":"Main","output":{"bitrateKbps":128}}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var state station.State
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if state.Config.ID != "main" || state.Config.StreamURL != "http://icecast.test/stations/main.mp3" {
		t.Fatalf("state = %#v", state.Config)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/stations/main", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/v1/stations/main", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestQueueRevisionValidationAPI(t *testing.T) {
	manager := newTestManager(t, 2)
	defer stopTestManager(t, manager)
	handler := NewServer(":0", manager, "").routes()
	createStation(t, handler, "main")

	body := `{"revision":1,"tracks":[{"itemId":"one","media":{"bucket":"music","objectKey":"one.mp3"}}]}`
	req := httptest.NewRequest(http.MethodPut, "/v1/stations/main/queue", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("queue status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPut, "/v1/stations/main/queue", bytes.NewBufferString(body))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale queue status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAnnouncementDedupAPI(t *testing.T) {
	manager := newTestManager(t, 2)
	defer stopTestManager(t, manager)
	handler := NewServer(":0", manager, "").routes()
	createStation(t, handler, "main")

	body := `{"commandId":"ann-1","media":{"bucket":"alerts","objectKey":"a.wav"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/stations/main/announcements", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("announcement status = %d, body = %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/stations/main/announcements", bytes.NewBufferString(body))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCapacityAndAuthAPI(t *testing.T) {
	manager := newTestManager(t, 1)
	defer stopTestManager(t, manager)
	handler := NewServer(":0", manager, "secret").routes()

	req := httptest.NewRequest(http.MethodPut, "/v1/stations/main", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPut, "/v1/stations/main", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPut, "/v1/stations/second", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("capacity status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func createStation(t *testing.T, handler http.Handler, id string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/v1/stations/"+id, bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func newTestManager(t *testing.T, capacity int) *radio.Manager {
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
	manager, err := radio.NewManagerWithSourceFactory(cfg, media.NewMemoryStore(), audio.NewFFmpeg("ffmpeg", "", nil), &events.MemoryPublisher{}, nil, func(cfg icecast.Config) station.Source {
		return testSource{mountURL: cfg.URL + cfg.Mount}
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func stopTestManager(t *testing.T, manager *radio.Manager) {
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
