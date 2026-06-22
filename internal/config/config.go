package config

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"radio_engine/internal/icecast"
)

type Config struct {
	HTTPAddr     string
	AuthToken    string
	InstanceID   string
	MaxStations  int
	FFmpegPath   string
	FFprobePath  string
	ShutdownWait time.Duration
	Icecast      icecast.Config
	MinIO        MinIO
	Redis        Redis
	Cache        Cache
}

type MinIO struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Secure    bool
	Region    string
	Timeout   time.Duration
	Retries   int
}

type Redis struct {
	Address  string
	Password string
	DB       int
	Stream   string
	Timeout  time.Duration
}

type Cache struct {
	Dir      string
	MaxBytes int64
}

func Load() Config {
	return Config{
		HTTPAddr:     env("HTTP_ADDR", ":8080"),
		AuthToken:    strings.TrimSpace(os.Getenv("HTTP_BEARER_TOKEN")),
		InstanceID:   env("INSTANCE_ID", hostname()),
		MaxStations:  envInt("MAX_STATIONS", 20),
		FFmpegPath:   env("FFMPEG_PATH", "ffmpeg"),
		FFprobePath:  env("FFPROBE_PATH", "ffprobe"),
		ShutdownWait: time.Duration(envInt("SHUTDOWN_TIMEOUT_SECONDS", 15)) * time.Second,
		Icecast: icecast.Config{
			URL:         strings.TrimRight(env("ICECAST_URL", "http://127.0.0.1:8000"), "/"),
			SourceUser:  env("ICECAST_SOURCE_USER", "source"),
			SourcePass:  env("ICECAST_SOURCE_PASSWORD", "hackme"),
			Description: env("STATION_DESCRIPTION", "Radio Engine stream"),
			Genre:       env("STATION_GENRE", "Various"),
		},
		MinIO: MinIO{
			Endpoint:  env("MINIO_ENDPOINT", "127.0.0.1:9000"),
			AccessKey: env("MINIO_ACCESS_KEY", "minioadmin"),
			SecretKey: env("MINIO_SECRET_KEY", "minioadmin"),
			Secure:    envBool("MINIO_SECURE", false),
			Region:    strings.TrimSpace(os.Getenv("MINIO_REGION")),
			Timeout:   time.Duration(envInt("MINIO_TIMEOUT_SECONDS", 15)) * time.Second,
			Retries:   envInt("MINIO_RETRIES", 3),
		},
		Redis: Redis{
			Address:  env("REDIS_ADDR", "127.0.0.1:6379"),
			Password: os.Getenv("REDIS_PASSWORD"),
			DB:       envInt("REDIS_DB", 0),
			Stream:   env("REDIS_STREAM", "radio.events"),
			Timeout:  time.Duration(envInt("REDIS_TIMEOUT_SECONDS", 3)) * time.Second,
		},
		Cache: Cache{
			Dir:      env("MEDIA_CACHE_DIR", "/tmp/radio-engine-cache"),
			MaxBytes: envInt64("MEDIA_CACHE_MAX_BYTES", 5<<30),
		},
	}
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

func envInt64(name string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		log.Printf("invalid %s=%q, using %d", name, value, fallback)
		return fallback
	}
	return parsed
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		log.Printf("invalid %s=%q, using %t", name, value, fallback)
		return fallback
	}
	return parsed
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "radio-engine"
	}
	return name
}
