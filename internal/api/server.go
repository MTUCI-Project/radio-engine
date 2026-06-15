package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"radio_engine/internal/radio"
	"radio_engine/internal/station"
)

type Server struct {
	addr    string
	manager *radio.Manager
}

type commandRequest struct {
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
}

func NewServer(addr string, manager *radio.Manager) *Server {
	if addr == "" {
		addr = ":8080"
	}
	return &Server{addr: addr, manager: manager}
}

func (s *Server) Run(ctx context.Context) error {
	server := &http.Server{
		Addr:              s.addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		log.Printf("command API listening on %s", s.addr)
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
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/stations", s.handleStations)
	mux.HandleFunc("/stations/", s.handleStation)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStations(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/stations" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, s.manager.States())
}

func (s *Server) handleStation(w http.ResponseWriter, r *http.Request) {
	id, action, ok := stationPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch action {
	case "":
		s.handleStationState(w, r, id)
	case "skip":
		s.handleSkip(w, r, id)
	case "announcement":
		s.handleAnnouncement(w, r, id)
	case "commands":
		s.handleCommand(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleStationState(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}

	radioStation, ok := s.manager.Station(id)
	if !ok {
		writeError(w, http.StatusNotFound, "station not found")
		return
	}
	writeJSON(w, http.StatusOK, radioStation.State())
}

func (s *Server) handleSkip(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	s.enqueue(w, id, station.SkipTrack)
}

func (s *Server) handleAnnouncement(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	var req commandRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}

	s.enqueue(w, id, station.Command{
		Type: station.CommandPlayAnnouncement,
		Path: req.Path,
	})
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	var req commandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Type == "" {
		writeError(w, http.StatusBadRequest, "command type is required")
		return
	}

	s.enqueue(w, id, station.Command{Type: req.Type, Path: req.Path})
}

func (s *Server) enqueue(w http.ResponseWriter, id string, command station.Command) {
	err := s.manager.Command(id, command)
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, command)
	case errors.Is(err, radio.ErrStationNotFound):
		writeError(w, http.StatusNotFound, "station not found")
	case errors.Is(err, radio.ErrCommandQueueFull):
		writeError(w, http.StatusServiceUnavailable, "station command queue is full")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func stationPath(path string) (id string, action string, ok bool) {
	rest := strings.TrimPrefix(path, "/stations/")
	if rest == path || rest == "" {
		return "", "", false
	}

	parts := strings.Split(rest, "/")
	if len(parts) > 2 || parts[0] == "" {
		return "", "", false
	}
	if len(parts) == 2 {
		action = parts[1]
		if action == "" {
			return "", "", false
		}
	}
	return parts[0], action, true
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
