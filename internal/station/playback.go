package station

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"radio_engine/internal/audio"
	"radio_engine/internal/playlist"
)

type preparedTrack struct {
	track  playlist.Track
	asset  audio.PCMAsset
	reader *audio.PCMReader
}

type preparedAnnouncement struct {
	announcement playlist.Announcement
	asset        audio.PCMAsset
	reader       *audio.PCMReader
}

type prepareTrackResult struct {
	value *preparedTrack
	err   error
	track playlist.Track
}

type prepareAnnouncementResult struct {
	value        *preparedAnnouncement
	err          error
	announcement playlist.Announcement
}

func (s *Station) Run(ctx context.Context) error {
	s.emit("station.started", "", nil)
	defer s.emit("station.stopped", "", nil)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		conn, err := s.deps.Source.Connect(ctx)
		if err != nil {
			s.setError(err)
			s.emit("icecast.disconnected", "", map[string]any{"error": err.Error()})
			if s.deps.Metrics != nil {
				s.deps.Metrics.IcecastReconnects.WithLabelValues(s.ID).Inc()
			}
			if !sleep(ctx, s.deps.Reconnect) {
				return ctx.Err()
			}
			continue
		}
		s.setConnected(true)
		s.emit("icecast.connected", "", map[string]any{"streamUrl": s.deps.Source.MountURL()})
		err = s.runConnection(ctx, conn)
		_ = conn.Close()
		s.setConnected(false)
		if errors.Is(err, context.Canceled) {
			return err
		}
		s.setError(err)
		s.emit("icecast.disconnected", "", map[string]any{"error": errorString(err)})
		if s.deps.Metrics != nil {
			s.deps.Metrics.IcecastReconnects.WithLabelValues(s.ID).Inc()
		}
		if !sleep(ctx, s.deps.Reconnect) {
			return ctx.Err()
		}
	}
}

func (s *Station) runConnection(ctx context.Context, conn io.Writer) error {
	s.mu.Lock()
	output := s.config.Output
	profile := s.config.AnnouncementProfile
	s.mu.Unlock()
	encoder, err := s.deps.FFmpeg.StartEncoder(ctx, conn, output)
	if err != nil {
		return err
	}
	renderErr := s.render(ctx, encoder, profile)
	closeErr := encoder.Close()
	if renderErr != nil {
		return renderErr
	}
	return closeErr
}

func (s *Station) render(ctx context.Context, encoder *audio.Encoder, profile audio.AnnouncementProfile) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	mixer := audio.NewMixer(profile)

	var current, next *preparedTrack
	var announcement *preparedAnnouncement
	var trackPreparing bool
	var announcementPreparing bool
	trackReady := make(chan prepareTrackResult, 1)
	announcementReady := make(chan prepareAnnouncementResult, 1)
	var crossfadeTotal int64
	var crossfadeConsumed int64

	defer func() {
		closePreparedTrack(current)
		closePreparedTrack(next)
		closePreparedAnnouncement(announcement)
	}()

	for {
		if current == nil && next != nil && s.canPlayMusic() {
			current, next = next, nil
			s.trackStarted(current.track)
			crossfadeTotal, crossfadeConsumed = 0, 0
		}
		s.startTrackPrepare(ctx, current, next, &trackPreparing, trackReady)
		s.startAnnouncementPrepare(ctx, announcement, &announcementPreparing, announcementReady)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case command := <-s.commands:
			switch command.Type {
			case CommandPlay:
				s.mu.Lock()
				if s.mode == ModeStopped {
					s.mode = ModeIdle
				}
				s.mu.Unlock()
				s.emit("playback.resumed", command.CorrelationID, nil)
			case CommandStop:
				closePreparedTrack(current)
				current = nil
				crossfadeTotal, crossfadeConsumed = 0, 0
				s.mu.Lock()
				s.current = nil
				s.mode = ModeStopped
				s.mu.Unlock()
				s.emit("playback.stopped", command.CorrelationID, nil)
			case CommandSkip:
				if current != nil {
					s.emit("track.skipped", command.CorrelationID, map[string]any{"itemId": current.track.ItemID})
					closePreparedTrack(current)
					current = nil
					crossfadeTotal, crossfadeConsumed = 0, 0
					s.mu.Lock()
					s.current = nil
					s.mu.Unlock()
				}
			}
		case result := <-trackReady:
			trackPreparing = false
			if result.err != nil {
				s.mediaFailure("track", result.track.ItemID, result.err)
				continue
			}
			if current == nil && s.canPlayMusic() {
				current = result.value
				s.trackStarted(current.track)
			} else if next == nil {
				next = result.value
			} else {
				closePreparedTrack(result.value)
			}
		case result := <-announcementReady:
			announcementPreparing = false
			if result.err != nil {
				s.mediaFailure("announcement", result.announcement.CommandID, result.err)
				s.finishAnnouncement(result.announcement, "failed")
				continue
			}
			if announcement == nil {
				announcement = result.value
				s.announcementStarted(announcement.announcement)
			} else {
				closePreparedAnnouncement(result.value)
			}
		case <-s.wakeup:
		case <-ticker.C:
			s.mu.Lock()
			newProfile := s.config.AnnouncementProfile
			s.mu.Unlock()
			if newProfile != profile {
				profile = newProfile
				mixer.SetProfile(profile)
			}

			music, currentDone, transitionStarted, err := s.renderMusic(current, next, &crossfadeTotal, &crossfadeConsumed)
			if err != nil {
				return err
			}
			if transitionStarted && next != nil {
				s.emit("transition.started", "", map[string]any{
					"fromItemId": current.track.ItemID,
					"toItemId":   next.track.ItemID,
					"type":       next.track.TransitionIn.Type,
				})
			}

			annSamples, annDone, err := readBlock(announcement)
			if err != nil {
				s.mediaFailure("announcement", announcement.announcement.CommandID, err)
				annDone = true
			}
			out := mixer.Mix(music, annSamples, announcement != nil)
			if err := encoder.Write(out); err != nil {
				return fmt.Errorf("write PCM to encoder: %w", err)
			}

			if currentDone && current != nil {
				finished := current.track
				closePreparedTrack(current)
				current = nil
				if next != nil {
					current, next = next, nil
					s.trackFinished(finished)
					s.trackStarted(current.track)
				} else {
					s.trackFinished(finished)
					s.setCurrent(nil)
				}
				crossfadeTotal, crossfadeConsumed = 0, 0
			}
			if annDone && announcement != nil {
				finished := announcement.announcement
				closePreparedAnnouncement(announcement)
				announcement = nil
				s.finishAnnouncement(finished, "finished")
			}
		}
	}
}

func (s *Station) renderMusic(current, next *preparedTrack, total, consumed *int64) ([]float32, bool, bool, error) {
	if current == nil {
		return make([]float32, audio.BlockFrames*audio.Channels), false, false, nil
	}
	transitionStarted := false
	transition := playlist.Transition{Type: playlist.TransitionCut}
	if next != nil {
		transition = next.track.TransitionIn
	}
	transitionFrames := int64(transition.Duration().Seconds() * audio.SampleRate)
	inTransition := next != nil && transition.Type != playlist.TransitionCut && transitionFrames > 0 && current.reader.RemainingFrames() <= transitionFrames
	if inTransition && *total == 0 {
		*total = min(transitionFrames, current.reader.RemainingFrames())
		transitionStarted = true
	}
	currentSamples, currentDone, err := current.reader.ReadFrames(audio.BlockFrames)
	if err != nil {
		return nil, false, transitionStarted, err
	}
	if !inTransition {
		return padBlock(currentSamples), currentDone, transitionStarted, nil
	}

	progress := float64(*consumed) / float64(max(int64(1), *total))
	*consumed += int64(audio.BlockFrames)
	switch transition.Type {
	case playlist.TransitionCrossfade:
		nextSamples, _, readErr := next.reader.ReadFrames(audio.BlockFrames)
		if readErr != nil {
			return nil, false, transitionStarted, readErr
		}
		return padBlock(audio.ApplyTransition(currentSamples, nextSamples, progress, transition.Curve)), currentDone, transitionStarted, nil
	case playlist.TransitionFade:
		return padBlock(audio.Fade(currentSamples, progress, false, transition.Curve)), currentDone, transitionStarted, nil
	default:
		return padBlock(currentSamples), currentDone, transitionStarted, nil
	}
}

func (s *Station) startTrackPrepare(ctx context.Context, current, next *preparedTrack, preparing *bool, result chan<- prepareTrackResult) {
	if *preparing || next != nil {
		return
	}
	if current == nil && !s.canPlayMusic() {
		return
	}
	track, ok := s.popQueue()
	if !ok {
		return
	}
	*preparing = true
	go func() {
		value, err := s.prepareTrack(ctx, track)
		select {
		case result <- prepareTrackResult{value: value, err: err, track: track}:
		case <-ctx.Done():
			closePreparedTrack(value)
		}
	}()
}

func (s *Station) startAnnouncementPrepare(ctx context.Context, current *preparedAnnouncement, preparing *bool, result chan<- prepareAnnouncementResult) {
	if *preparing || current != nil {
		return
	}
	announcement, ok := s.popAnnouncement()
	if !ok {
		return
	}
	*preparing = true
	go func() {
		value, err := s.prepareAnnouncement(ctx, announcement)
		select {
		case result <- prepareAnnouncementResult{value: value, err: err, announcement: announcement}:
		case <-ctx.Done():
			closePreparedAnnouncement(value)
		}
	}()
}

func (s *Station) prepareTrack(ctx context.Context, track playlist.Track) (*preparedTrack, error) {
	asset, err := s.deps.Media.Resolve(ctx, track.Media)
	if err != nil {
		return nil, err
	}
	pcm, err := s.deps.FFmpeg.Decode(ctx, asset.Path)
	if err != nil {
		return nil, err
	}
	reader, err := audio.OpenPCM(pcm)
	if err != nil {
		pcm.Remove()
		return nil, err
	}
	return &preparedTrack{track: track, asset: pcm, reader: reader}, nil
}

func (s *Station) prepareAnnouncement(ctx context.Context, announcement playlist.Announcement) (*preparedAnnouncement, error) {
	asset, err := s.deps.Media.Resolve(ctx, announcement.Media)
	if err != nil {
		return nil, err
	}
	pcm, err := s.deps.FFmpeg.Decode(ctx, asset.Path)
	if err != nil {
		return nil, err
	}
	reader, err := audio.OpenPCM(pcm)
	if err != nil {
		pcm.Remove()
		return nil, err
	}
	return &preparedAnnouncement{announcement: announcement, asset: pcm, reader: reader}, nil
}

func (s *Station) popQueue() (playlist.Track, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == ModeStopped || len(s.queue) == 0 {
		if s.mode != ModeStopped && s.current == nil {
			s.mode = ModeIdle
		}
		return playlist.Track{}, false
	}
	track := s.queue[0]
	s.queue = append([]playlist.Track(nil), s.queue[1:]...)
	remaining := len(s.queue)
	if remaining <= 2 {
		go s.emit("queue.low", "", map[string]any{"remaining": remaining})
	}
	if remaining == 0 {
		go s.emit("queue.empty", "", nil)
	}
	return track, true
}

func (s *Station) popAnnouncement() (playlist.Announcement, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.announcements) == 0 {
		return playlist.Announcement{}, false
	}
	announcement := s.announcements[0]
	s.announcements = append([]playlist.Announcement(nil), s.announcements[1:]...)
	return announcement, true
}

func (s *Station) canPlayMusic() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode != ModeStopped
}

func (s *Station) trackStarted(track playlist.Track) {
	s.mu.Lock()
	s.current = &track
	s.mode = ModePlaying
	s.startedAt = time.Now().UTC()
	s.lastError = ""
	s.mu.Unlock()
	if s.deps.Metrics != nil {
		s.deps.Metrics.TrackEvents.WithLabelValues(s.ID, "started").Inc()
	}
	s.emit("track.started", "", map[string]any{"track": track})
}

func (s *Station) trackFinished(track playlist.Track) {
	if s.deps.Metrics != nil {
		s.deps.Metrics.TrackEvents.WithLabelValues(s.ID, "finished").Inc()
	}
	s.emit("track.finished", "", map[string]any{"track": track})
}

func (s *Station) setCurrent(track *playlist.Track) {
	s.mu.Lock()
	s.current = track
	if track == nil && s.mode != ModeStopped {
		s.mode = ModeIdle
	}
	s.mu.Unlock()
}

func (s *Station) announcementStarted(announcement playlist.Announcement) {
	s.mu.Lock()
	s.currentAnn = &announcement
	s.mu.Unlock()
	if s.deps.Metrics != nil {
		s.deps.Metrics.Announcements.WithLabelValues(s.ID, "started").Inc()
	}
	s.emit("announcement.started", announcement.CorrelationID, map[string]any{"commandId": announcement.CommandID})
}

func (s *Station) finishAnnouncement(announcement playlist.Announcement, result string) {
	s.mu.Lock()
	s.currentAnn = nil
	s.mu.Unlock()
	if s.deps.Metrics != nil {
		s.deps.Metrics.Announcements.WithLabelValues(s.ID, result).Inc()
	}
	s.emit("announcement."+result, announcement.CorrelationID, map[string]any{"commandId": announcement.CommandID})
}

func (s *Station) mediaFailure(kind, id string, err error) {
	s.setError(err)
	if s.deps.Metrics != nil {
		s.deps.Metrics.MediaFailures.WithLabelValues(s.ID, kind).Inc()
	}
	s.emit(kind+".failed", "", map[string]any{"id": id, "error": err.Error()})
}

func (s *Station) setError(err error) {
	s.mu.Lock()
	s.lastError = errorString(err)
	s.mu.Unlock()
}

func readBlock(announcement *preparedAnnouncement) ([]float32, bool, error) {
	if announcement == nil {
		return nil, false, nil
	}
	samples, done, err := announcement.reader.ReadFrames(audio.BlockFrames)
	return padBlock(samples), done, err
}

func padBlock(samples []float32) []float32 {
	size := audio.BlockFrames * audio.Channels
	if len(samples) >= size {
		return samples
	}
	result := make([]float32, size)
	copy(result, samples)
	return result
}

func closePreparedTrack(track *preparedTrack) {
	if track == nil {
		return
	}
	if track.reader != nil {
		_ = track.reader.Close()
	}
	track.asset.Remove()
}

func closePreparedAnnouncement(announcement *preparedAnnouncement) {
	if announcement == nil {
		return
	}
	if announcement.reader != nil {
		_ = announcement.reader.Close()
	}
	announcement.asset.Remove()
}

func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
