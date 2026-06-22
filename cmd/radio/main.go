package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"radio_engine/internal/api"
	"radio_engine/internal/config"
	"radio_engine/internal/radio"
)

func main() {
	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	manager, err := 
	radio.NewManager(cfg.Stations)
	if err != nil {
		log.Fatalf("create radio manager: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	log.Printf("starting %d station(s)", len(cfg.Stations))
	errs := make(chan error, 2)
	go func() {
		errs <- manager.Run(runCtx)
	}()
	go func() {
		errs <- api.NewServer(cfg.HTTPAddr, manager).Run(runCtx)
	}()

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

	log.Println("shutdown requested")
}
