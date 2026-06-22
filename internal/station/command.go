package station

import "radio_engine/internal/playlist"

type CommandType string

const (
	CommandPlay CommandType = "play"
	CommandStop CommandType = "stop"
	CommandSkip CommandType = "skip"
)

type Command struct {
	Type          CommandType
	CorrelationID string
}

type AnnouncementCommand struct {
	Announcement playlist.Announcement
}
