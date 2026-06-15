package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"radio_engine/internal/icecast"
	"radio_engine/internal/station"
)

type Config struct {
	Stations []StationConfig
	HTTPAddr string
}

type StationConfig struct {
	Icecast icecast.Config
	Station station.Config
}

func Load() Config {
	ids := stationIDs()
	stations := make([]StationConfig, 0, len(ids))

	for _, id := range ids {
		prefix := stationEnvPrefix(id)
		singleStation := len(ids) == 1
		name := stationEnv(prefix, "NAME", env("STATION_NAME", fmt.Sprintf("Radio Engine %s", id)))

		mountFallback := "/" + id + ".mp3"
		if singleStation {
			mountFallback = env("ICECAST_MOUNT", "/radio.mp3")
		}
		mount := stationEnv(prefix, "MOUNT", mountFallback)
		if !strings.HasPrefix(mount, "/") {
			mount = "/" + mount
		}

		stations = append(stations, StationConfig{
			Icecast: icecast.Config{
				URL:         strings.TrimRight(env("ICECAST_URL", "http://127.0.0.1:8000"), "/"),
				SourceUser:  env("ICECAST_SOURCE_USER", "source"),
				SourcePass:  env("ICECAST_SOURCE_PASSWORD", "hackme"),
				Mount:       mount,
				Name:        name,
				Description: stationEnv(prefix, "DESCRIPTION", env("STATION_DESCRIPTION", "Go MP3 radio stream")),
				Genre:       stationEnv(prefix, "GENRE", env("STATION_GENRE", "Various")),
				Public:      false,
			},
			Station: station.Config{
				ID:               id,
				Name:             name,
				PlaylistDir:      stationEnv(prefix, "PLAYLIST_DIR", env("PLAYLIST_DIR", "tracklist")),
				AnnouncementsDir: stationEnv(prefix, "ANNOUNCEMENTS_DIR", env("ANNOUNCEMENTS_DIR", "")),
				InitialTrackPath: stationEnv(prefix, "TRACK_PATH", env("TRACK_PATH", "")),
				ReconnectDelay:   time.Duration(stationEnvInt(prefix, "RECONNECT_DELAY_SECONDS", envInt("RECONNECT_DELAY_SECONDS", 3))) * time.Second,
			},
		})
	}

	return Config{
		Stations: stations,
		HTTPAddr: env("HTTP_ADDR", ":8080"),
	}
}

func stationIDs() []string {
	if configured := strings.TrimSpace(os.Getenv("STATIONS")); configured != "" {
		return splitCSV(configured)
	}

	count := envInt("STATION_COUNT", 0)
	if count > 0 {
		ids := make([]string, 0, count)
		for i := 1; i <= count; i++ {
			ids = append(ids, fmt.Sprintf("station-%d", i))
		}
		return ids
	}

	return []string{env("STATION_ID", "default")}
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		items = append(items, item)
	}
	if len(items) == 0 {
		return []string{"default"}
	}
	return items
}

func stationEnvPrefix(id string) string {
	var b strings.Builder
	b.WriteString("STATION_")
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToUpper(r))
			continue
		}
		b.WriteByte('_')
	}
	b.WriteByte('_')
	return b.String()
}

func stationEnv(prefix, name, fallback string) string {
	return env(prefix+name, fallback)
}

func stationEnvInt(prefix, name string, fallback int) int {
	return envInt(prefix+name, fallback)
}

func env(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("invalid %s=%q, using %d", name, value, fallback)
		return fallback
	}
	return parsed
}
