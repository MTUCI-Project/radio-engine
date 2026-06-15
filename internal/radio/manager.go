package radio

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"

	"radio_engine/internal/config"
	"radio_engine/internal/icecast"
	"radio_engine/internal/station"
)

type Manager struct {
	stations map[string]*station.Station
	delays   map[string]config.StationConfig
}

var (
	ErrStationNotFound  = errors.New("station not found")
	ErrCommandQueueFull = errors.New("station command queue is full")
)

func NewManager(configs []config.StationConfig) (*Manager, error) {
	manager := &Manager{
		stations: make(map[string]*station.Station, len(configs)),
		delays:   make(map[string]config.StationConfig, len(configs)),
	}

	for _, cfg := range configs {
		if _, exists := manager.stations[cfg.Station.ID]; exists {
			return nil, fmt.Errorf("duplicate station id %q", cfg.Station.ID)
		}

		source := icecast.NewSource(cfg.Icecast)
		radioStation, err := station.New(cfg.Station, source)
		if err != nil {
			return nil, fmt.Errorf("create station %q: %w", cfg.Station.ID, err)
		}

		manager.stations[radioStation.ID] = radioStation
		manager.delays[radioStation.ID] = cfg
	}

	return manager, nil
}

func (m *Manager) Station(id string) (*station.Station, bool) {
	radioStation, ok := m.stations[id]
	return radioStation, ok
}

func (m *Manager) Stations() map[string]*station.Station {
	stations := make(map[string]*station.Station, len(m.stations))
	for id, radioStation := range m.stations {
		stations[id] = radioStation
	}
	return stations
}

func (m *Manager) States() []station.State {
	states := make([]station.State, 0, len(m.stations))
	for _, radioStation := range m.stations {
		states = append(states, radioStation.State())
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].ID < states[j].ID
	})
	return states
}

func (m *Manager) Command(id string, command station.Command) error {
	radioStation, ok := m.Station(id)
	if !ok {
		return ErrStationNotFound
	}

	select {
	case radioStation.Commands <- command:
		return nil
	default:
		return ErrCommandQueueFull
	}
}

func (m *Manager) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make(chan error, len(m.stations))

	for id, radioStation := range m.stations {
		cfg := m.delays[id]
		wg.Add(1)
		go func(id string, radioStation *station.Station, cfg config.StationConfig) {
			defer wg.Done()
			log.Printf("starting station %q (%s)", radioStation.Name, radioStation.ID)
			if err := radioStation.Run(ctx, cfg.Station.ReconnectDelay); err != nil && ctx.Err() == nil {
				errs <- fmt.Errorf("station %s stopped: %w", id, err)
			}
		}(id, radioStation, cfg)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		<-done
		return ctx.Err()
	case err := <-errs:
		return err
	case <-done:
		return nil
	}
}
