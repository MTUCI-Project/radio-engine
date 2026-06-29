package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"

	"radio_engine/internal/api"
	"radio_engine/internal/audio"
	"radio_engine/internal/config"
	"radio_engine/internal/events"
	"radio_engine/internal/media"
	"radio_engine/internal/observability"
	"radio_engine/internal/radio"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{})))
	cfg := config.Load()

	metrics := observability.New(prometheus.DefaultRegisterer)
	ffmpeg := audio.NewFFmpeg(cfg.FFmpegPath, os.TempDir(), func(count int64) {
		metrics.FFmpegProcesses.Set(float64(count))
	})
	store, err := media.NewMinIOStore(cfg.MinIO, cfg.Cache)
	if err != nil {
		log.Fatalf("create media store: %v", err)
	}
	dispatcher := events.NewDispatcher(cfg.Redis, cfg.InstanceID, 8192)
	manager, err := radio.NewManager(cfg, store, ffmpeg, dispatcher, metrics)
	if err != nil {
		log.Fatalf("create radio manager: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	slog.Info("starting radio engine", "instance_id", cfg.InstanceID, "max_stations", cfg.MaxStations)
	errs := make(chan error, 2)
	go func() { errs <- manager.Run(runCtx) }()
	go func() { errs <- api.NewServer(cfg.HTTPAddr, manager, cfg.AuthToken).Run(runCtx) }()

	var shutdownErr error
	for pending := 2; pending > 0; pending-- {
		err := <-errs
		if err != nil && !errors.Is(err, context.Canceled) && shutdownErr == nil {
			shutdownErr = err
			cancel()
		}
	}
	if shutdownErr != nil {
		log.Fatalf("radio stopped: %v", shutdownErr)
	}
	slog.Info("shutdown complete")
}
