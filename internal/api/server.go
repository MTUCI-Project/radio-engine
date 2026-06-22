package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"radio_engine/internal/audio"
	"radio_engine/internal/playlist"
	"radio_engine/internal/radio"
	"radio_engine/internal/station"
)

type Server struct {
	addr      string
	manager   *radio.Manager
	authToken string
}

type stationRequest struct {
	Name                string                    `json:"name,omitempty"`
	Description         string                    `json:"description,omitempty"`
	Genre               string                    `json:"genre,omitempty"`
	Public              bool                      `json:"public,omitempty"`
	Mount               string                    `json:"mount,omitempty"`
	Output              audio.OutputConfig        `json:"output,omitempty"`
	TransitionDefaults  playlist.Transition       `json:"transitionDefaults,omitempty"`
	AnnouncementProfile audio.AnnouncementProfile `json:"announcementProfile,omitempty"`
	CorrelationID       string                    `json:"correlationId,omitempty"`
}

type queueRequest struct {
	Revision      uint64           `json:"revision"`
	Tracks        []playlist.Track `json:"tracks"`
	CorrelationID string           `json:"correlationId,omitempty"`
}

type announcementRequest struct {
	CommandID     string            `json:"commandId"`
	Media         playlist.MediaRef `json:"media"`
	Title         string            `json:"title,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CorrelationID string            `json:"correlationId,omitempty"`
}

type commandRequest struct {
	CorrelationID string `json:"correlationId,omitempty"`
}

type errorEnvelope struct {
	Error requestError `json:"error"`
}

type requestError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
}

func NewServer(addr string, manager *radio.Manager, authToken string) *Server {
	if addr == "" {
		addr = ":8080"
	}
	return &Server{addr: addr, manager: manager, authToken: authToken}
}

func (s *Server) Run(ctx context.Context) error {
	server := &http.Server{
		Addr:              s.addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		slog.Info("command API listening", "addr", s.addr)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errs <- err
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown command API: %w", err)
		}
		if err := <-errs; err != nil {
			return err
		}
		return ctx.Err()
	case err := <-errs:
		return err
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", s.handleLive)
	mux.HandleFunc("/health/ready", s.handleReady)
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/v1/", s.requireAuth(http.HandlerFunc(s.handleV1)))
	return s.requestID(mux)
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, r)
		return
	}
	status := s.manager.Ready(r.Context())
	code := http.StatusOK
	if !status.Ready {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, status)
}

func (s *Server) handleV1(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/stations" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, r)
			return
		}
		writeJSON(w, http.StatusOK, s.manager.States())
		return
	}
	id, action, ok := stationPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "":
		s.handleStation(w, r, id)
	case "queue":
		s.handleQueue(w, r, id)
	case "announcements":
		s.handleAnnouncement(w, r, id)
	case "play":
		s.handleCommand(w, r, id, station.CommandPlay)
	case "stop":
		s.handleCommand(w, r, id, station.CommandStop)
	case "skip":
		s.handleCommand(w, r, id, station.CommandSkip)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleStation(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		radioStation, ok := s.manager.Station(id)
		if !ok {
			writeError(w, r, http.StatusNotFound, "station_not_found", "station not found")
			return
		}
		writeJSON(w, http.StatusOK, radioStation.State())
	case http.MethodPut:
		var req stationRequest
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		state, created, err := s.manager.UpsertStation(radio.UpsertStationRequest{
			ID:                  id,
			Name:                req.Name,
			Description:         req.Description,
			Genre:               req.Genre,
			Public:              req.Public,
			Mount:               req.Mount,
			Output:              req.Output,
			TransitionDefaults:  req.TransitionDefaults,
			AnnouncementProfile: req.AnnouncementProfile,
			CorrelationID:       correlationID(r, req.CorrelationID),
		})
		switch {
		case err == nil:
			if created {
				writeJSON(w, http.StatusCreated, state)
			} else {
				writeJSON(w, http.StatusOK, state)
			}
		case errors.Is(err, radio.ErrCapacityExceeded):
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":    requestError{Code: "capacity_exceeded", Message: err.Error(), RequestID: requestID(r)},
				"capacity": s.manager.Capacity(),
			})
		default:
			writeError(w, r, http.StatusBadRequest, "invalid_station", err.Error())
		}
	case http.MethodDelete:
		err := s.manager.DeleteStation(id, correlationID(r, ""))
		if errors.Is(err, radio.ErrStationNotFound) {
			writeError(w, r, http.StatusNotFound, "station_not_found", "station not found")
			return
		}
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "delete_failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w, r)
	}
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w, r)
		return
	}
	var req queueRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Revision == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_revision", "revision must be greater than zero")
		return
	}
	state, err := s.manager.ReplaceQueue(id, req.Revision, req.Tracks, correlationID(r, req.CorrelationID))
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, state)
	case errors.Is(err, radio.ErrStationNotFound):
		writeError(w, r, http.StatusNotFound, "station_not_found", "station not found")
	case errors.Is(err, radio.ErrQueueTooLarge):
		writeError(w, r, http.StatusBadRequest, "queue_too_large", fmt.Sprintf("queue can contain at most %d tracks", station.MaxQueueTracks))
	case errors.Is(err, radio.ErrStaleRevision):
		writeError(w, r, http.StatusConflict, "stale_revision", "queue revision must be newer than current revision")
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_queue", err.Error())
	}
}

func (s *Server) handleAnnouncement(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r)
		return
	}
	var req announcementRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	queued, err := s.manager.AddAnnouncement(id, playlist.Announcement{
		CommandID:     req.CommandID,
		Media:         req.Media,
		Title:         req.Title,
		Metadata:      req.Metadata,
		CorrelationID: correlationID(r, req.CorrelationID),
	})
	switch {
	case err == nil:
		status := http.StatusAccepted
		if !queued {
			status = http.StatusOK
		}
		writeJSON(w, status, map[string]any{"queued": queued})
	case errors.Is(err, radio.ErrStationNotFound):
		writeError(w, r, http.StatusNotFound, "station_not_found", "station not found")
	case errors.Is(err, station.ErrAnnouncementQueue):
		writeError(w, r, http.StatusServiceUnavailable, "announcement_queue_full", err.Error())
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_announcement", err.Error())
	}
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request, id string, commandType station.CommandType) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, r)
		return
	}
	var req commandRequest
	if err := decodeOptionalJSON(w, r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	err := s.manager.Command(id, station.Command{Type: commandType, CorrelationID: correlationID(r, req.CorrelationID)})
	switch {
	case err == nil:
		w.WriteHeader(http.StatusAccepted)
	case errors.Is(err, radio.ErrStationNotFound):
		writeError(w, r, http.StatusNotFound, "station_not_found", "station not found")
	case errors.Is(err, radio.ErrCommandQueueFull):
		writeError(w, r, http.StatusServiceUnavailable, "command_queue_full", "station command queue is full")
	default:
		writeError(w, r, http.StatusInternalServerError, "command_failed", err.Error())
	}
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	if s.authToken == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.authToken {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = fmt.Sprintf("%d", time.Now().UnixNano())
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func stationPath(path string) (id string, action string, ok bool) {
	rest := strings.TrimPrefix(path, "/v1/stations/")
	if rest == path || rest == "" {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		return "", "", false
	}
	if len(parts) == 2 {
		action = parts[1]
	}
	return parts[0], action, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("body must contain a single JSON object")
	}
	return nil
}

func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, value any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	return decodeJSON(w, r, value)
}

func methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusNoContent {
		return
	}
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Warn("write JSON response", "error", err)
	}
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: requestError{Code: code, Message: message, RequestID: requestID(r)}})
}

func correlationID(r *http.Request, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return requestID(r)
}

func requestID(r *http.Request) string {
	if value, ok := r.Context().Value(requestIDKey{}).(string); ok {
		return value
	}
	return r.Header.Get("X-Request-ID")
}

type requestIDKey struct{}
