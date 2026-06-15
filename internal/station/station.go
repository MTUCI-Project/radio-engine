package station

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"radio_engine/internal/mp3stream"
	"radio_engine/internal/playlist"
)

type Source interface {
	MountURL() string
	Connect(context.Context) (io.WriteCloser, error)
}

type Config struct {
	ID               string
	Name             string
	PlaylistDir      string
	AnnouncementsDir string
	InitialTrackPath string
	ReconnectDelay   time.Duration
}

type State struct {
	ID           string
	Name         string
	CurrentTrack playlist.Track
	Playing      bool
	StartedAt    time.Time
}

type Station struct {
	ID       string
	Name     string
	Commands chan Command

	source        Source
	playlist      *playlist.Playlist
	announcements *playlist.Playlist
	streamer      *mp3stream.Streamer

	mu    sync.RWMutex
	state State
}

func New(cfg Config, source Source) (*Station, error) {
	if cfg.ID == "" {
		cfg.ID = "default"
	}
	if cfg.Name == "" {
		cfg.Name = cfg.ID
	}

	var list *playlist.Playlist
	var err error
	if cfg.InitialTrackPath != "" {
		list = playlist.Single(cfg.InitialTrackPath)
	} else {
		list, err = playlist.FromDir(cfg.PlaylistDir)
		if err != nil {
			return nil, fmt.Errorf("load playlist: %w", err)
		}
	}

	if cfg.ReconnectDelay <= 0 {
		cfg.ReconnectDelay = 3 * time.Second
	}

	var announcements *playlist.Playlist
	if cfg.AnnouncementsDir != "" {
		announcements, err = playlist.FromDir(cfg.AnnouncementsDir)
		if err != nil {
			return nil, fmt.Errorf("load announcements: %w", err)
		}
	}

	station := &Station{
		ID:            cfg.ID,
		Name:          cfg.Name,
		Commands:      make(chan Command, 16),
		source:        source,
		playlist:      list,
		announcements: announcements,
		streamer:      mp3stream.NewStreamer(),
	}
	station.state = State{ID: cfg.ID, Name: cfg.Name}
	return station, nil
}

func (s *Station) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Station) Run(ctx context.Context, reconnectDelay time.Duration) error {
	if reconnectDelay <= 0 {
		reconnectDelay = 3 * time.Second
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		conn, err := s.source.Connect(ctx)
		if err != nil {
			log.Printf("station %s: source connection failed: %v", s.ID, err)
			if err := sleep(ctx, reconnectDelay); err != nil {
				return err
			}
			continue
		}

		log.Printf("station %s: connected, mount is available at %s", s.ID, s.source.MountURL())
		stopCloseOnCancel := closeOnCancel(ctx, conn)
		err = s.playContinuously(ctx, conn)
		conn.Close()
		stopCloseOnCancel()
		s.setStopped()

		if errors.Is(err, context.Canceled) {
			return err
		}
		log.Printf("station %s: stream connection ended: %v", s.ID, err)
		if err := sleep(ctx, reconnectDelay); err != nil {
			return err
		}
	}
}

func (s *Station) playContinuously(ctx context.Context, dst io.Writer) error {
	var pending []Command
	session := s.streamer.NewSession()

	for {
		track, isAnnouncement := s.nextTrack(pending)
		pending = nil

		s.setPlaying(track)
		if isAnnouncement {
			log.Printf("station %s: playing announcement %q", s.ID, track.Path)
		} else {
			log.Printf("station %s: playing track %q", s.ID, track.Path)
		}

		err, queued := s.playTrack(ctx, dst, session, track)
		pending = append(pending, queued...)

		if errors.Is(err, mp3stream.ErrSkipped) {
			log.Printf("station %s: skipped %q", s.ID, track.Path)
			continue
		}
		if err != nil {
			return err
		}
	}
}

func (s *Station) nextTrack(commands []Command) (playlist.Track, bool) {
	for _, command := range commands {
		if command.Type != CommandPlayAnnouncement {
			continue
		}
		if command.Path != "" {
			return playlist.Track{Path: command.Path, Title: command.Path}, true
		}
		if s.announcements != nil {
			return s.announcements.Next(), true
		}
		log.Printf("station %s: announcement requested but no path or announcements playlist is configured", s.ID)
	}
	return s.playlist.Next(), false
}

func (s *Station) playTrack(ctx context.Context, dst io.Writer, session *mp3stream.Session, track playlist.Track) (error, []Command) {
	file, err := os.Open(track.Path)
	if err != nil {
		return fmt.Errorf("open track %q: %w", track.Path, err), nil
	}
	defer file.Close()

	skip := make(chan struct{})
	done := make(chan error, 1)
	var queued []Command
	closeSkip := sync.OnceFunc(func() {
		close(skip)
	})

	go func() {
		done <- session.Stream(ctx, dst, file, skip)
	}()

	for {
		select {
		case <-ctx.Done():
			closeSkip()
			return ctx.Err(), queued
		case err := <-done:
			return err, queued
		case command := <-s.Commands:
			switch command.Type {
			case CommandSkipTrack:
				closeSkip()
				return s.waitForStoppedStream(ctx, done, queued)
			case CommandPlayAnnouncement:
				queued = append(queued, command)
				closeSkip()
				return s.waitForStoppedStream(ctx, done, queued)
			default:
				log.Printf("station %s: ignoring unknown command %q", s.ID, command.Type)
			}
		}
	}
}

func (s *Station) waitForStoppedStream(ctx context.Context, done <-chan error, queued []Command) (error, []Command) {
	select {
	case <-ctx.Done():
		return ctx.Err(), queued
	case err := <-done:
		if err == nil {
			return mp3stream.ErrSkipped, queued
		}
		return err, queued
	}
}

func (s *Station) setPlaying(track playlist.Track) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state.CurrentTrack = track
	s.state.Playing = true
	s.state.StartedAt = time.Now()
}

func (s *Station) setStopped() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state.Playing = false
}

func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func closeOnCancel(ctx context.Context, closer io.Closer) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			closer.Close()
		case <-stop:
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}
