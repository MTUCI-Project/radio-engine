package station

const (
	CommandSkipTrack        = "skip_track"
	CommandPlayAnnouncement = "play_announcement"
)

var (
	SkipTrack        = Command{Type: CommandSkipTrack}
	PlayAnnouncement = Command{Type: CommandPlayAnnouncement}
)

type Command struct {
	Type string
	Path string
}
