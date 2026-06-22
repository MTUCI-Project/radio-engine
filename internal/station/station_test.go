package station

import (
	"context"
	"io"
	"testing"

	"radio_engine/internal/audio"
	"radio_engine/internal/events"
	"radio_engine/internal/media"
	"radio_engine/internal/playlist"
)

func TestReplaceQueueRevisionAndLimit(t *testing.T) {
	radioStation := newStationForTest(t)
	tracks := []playlist.Track{
		testTrack("1"),
		testTrack("2"),
	}
	state, err := radioStation.ReplaceQueue(1, tracks, "req-1")
	if err != nil {
		t.Fatalf("replace queue: %v", err)
	}
	if state.Queue.Revision != 1 || len(state.Queue.Tracks) != 2 {
		t.Fatalf("queue state = %#v", state.Queue)
	}
	if _, err := radioStation.ReplaceQueue(1, tracks, "req-2"); err != ErrStaleRevision {
		t.Fatalf("stale revision error = %v, want ErrStaleRevision", err)
	}
	tooMany := []playlist.Track{testTrack("1"), testTrack("2"), testTrack("3"), testTrack("4"), testTrack("5"), testTrack("6")}
	if _, err := radioStation.ReplaceQueue(2, tooMany, "req-3"); err != ErrQueueTooLarge {
		t.Fatalf("too large error = %v, want ErrQueueTooLarge", err)
	}
}

func TestAnnouncementDeduplication(t *testing.T) {
	radioStation := newStationForTest(t)
	announcement := playlist.Announcement{
		CommandID: "ann-1",
		Media:     playlist.MediaRef{Bucket: "b", ObjectKey: "ann.mp3"},
	}
	queued, err := radioStation.AddAnnouncement(announcement)
	if err != nil || !queued {
		t.Fatalf("add announcement queued=%t err=%v", queued, err)
	}
	queued, err = radioStation.AddAnnouncement(announcement)
	if err != nil {
		t.Fatalf("duplicate announcement: %v", err)
	}
	if queued {
		t.Fatal("duplicate announcement was queued")
	}
	if got := radioStation.State().PendingAnnouncements; got != 1 {
		t.Fatalf("pending announcements = %d, want 1", got)
	}
}

func TestStopDoesNotClearAnnouncementQueue(t *testing.T) {
	radioStation := newStationForTest(t)
	if err := radioStation.Send(Command{Type: CommandStop}); err != nil {
		t.Fatalf("send stop: %v", err)
	}
	announcement := playlist.Announcement{CommandID: "ann-1", Media: playlist.MediaRef{Bucket: "b", ObjectKey: "ann.mp3"}}
	if _, err := radioStation.AddAnnouncement(announcement); err != nil {
		t.Fatalf("add announcement: %v", err)
	}
	if got := radioStation.State().PendingAnnouncements; got != 1 {
		t.Fatalf("pending announcements = %d, want 1", got)
	}
}

func newStationForTest(t *testing.T) *Station {
	t.Helper()
	store := media.NewMemoryStore()
	store.Put(playlist.MediaRef{Bucket: "b", ObjectKey: "track-1.mp3"}, media.Asset{Path: "/tmp/missing"})
	radioStation, err := New(Config{
		ID:                 "test",
		Name:               "Test",
		Mount:              "/stations/test.mp3",
		TransitionDefaults: playlist.DefaultTransition(),
	}, Dependencies{
		Source: testSource{},
		Media:  store,
		FFmpeg: audio.NewFFmpeg("ffmpeg", "", nil),
		Events: &events.MemoryPublisher{},
	})
	if err != nil {
		t.Fatalf("new station: %v", err)
	}
	return radioStation
}

func testTrack(id string) playlist.Track {
	return playlist.Track{ItemID: id, Media: playlist.MediaRef{Bucket: "b", ObjectKey: "track-" + id + ".mp3"}}
}

type testSource struct{}

func (testSource) MountURL() string { return "http://icecast.test/stations/test.mp3" }
func (testSource) Connect(context.Context) (io.WriteCloser, error) {
	return discardWriteCloser{}, nil
}

type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }
