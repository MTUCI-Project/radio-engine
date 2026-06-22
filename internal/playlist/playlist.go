package playlist

import (
	"errors"
	"path"
	"strings"
	"time"
)

const MaxQueueTracks = 5

type MediaRef struct {
	Bucket    string `json:"bucket"`
	ObjectKey string `json:"objectKey"`
	VersionID string `json:"versionId,omitempty"`
}

func (m MediaRef) Validate() error {
	if strings.TrimSpace(m.Bucket) == "" {
		return errors.New("media.bucket is required")
	}
	if strings.TrimSpace(m.ObjectKey) == "" {
		return errors.New("media.objectKey is required")
	}
	return nil
}

type Transition struct {
	Type       string `json:"type"`
	DurationMS int    `json:"durationMs,omitempty"`
	Curve      string `json:"curve,omitempty"`
}

const (
	TransitionCut       = "cut"
	TransitionFade      = "fade"
	TransitionCrossfade = "crossfade"
)

func DefaultTransition() Transition {
	return Transition{Type: TransitionCrossfade, DurationMS: 2500, Curve: "equal_power"}
}

func (t Transition) Normalize(fallback Transition) Transition {
	if t.Type == "" {
		t = fallback
	}
	if t.Type == "" {
		t.Type = TransitionCut
	}
	switch t.Type {
	case TransitionCut:
		t.DurationMS = 0
	case TransitionFade, TransitionCrossfade:
		if t.DurationMS <= 0 {
			t.DurationMS = 1500
		}
		if t.DurationMS > 30000 {
			t.DurationMS = 30000
		}
		if t.Curve == "" {
			t.Curve = "equal_power"
		}
	default:
		t.Type = TransitionCut
		t.DurationMS = 0
	}
	return t
}

func (t Transition) Duration() time.Duration {
	return time.Duration(t.DurationMS) * time.Millisecond
}

type Track struct {
	ItemID       string            `json:"itemId"`
	Media        MediaRef          `json:"media"`
	Title        string            `json:"title,omitempty"`
	Artist       string            `json:"artist,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	TransitionIn Transition        `json:"transitionIn,omitempty"`
}

func (t Track) Normalize(fallback Transition) (Track, error) {
	if strings.TrimSpace(t.ItemID) == "" {
		return Track{}, errors.New("track.itemId is required")
	}
	if err := t.Media.Validate(); err != nil {
		return Track{}, err
	}
	if t.Title == "" {
		t.Title = TitleFromObjectKey(t.Media.ObjectKey)
	}
	t.TransitionIn = t.TransitionIn.Normalize(fallback)
	if t.Metadata == nil {
		t.Metadata = map[string]string{}
	}
	return t, nil
}

type Announcement struct {
	CommandID     string            `json:"commandId"`
	Media         MediaRef          `json:"media"`
	Title         string            `json:"title,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CorrelationID string            `json:"-"`
}

func (a Announcement) Validate() error {
	if strings.TrimSpace(a.CommandID) == "" {
		return errors.New("commandId is required")
	}
	return a.Media.Validate()
}

func TitleFromObjectKey(objectKey string) string {
	base := path.Base(objectKey)
	ext := path.Ext(base)
	return strings.TrimSuffix(base, ext)
}
