package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/time/rate"
	"libdb.so/persist"
)

type State struct {
	Rate  *rate.Limiter
	value persist.MustValue[state]
}

type state struct {
	Paused       bool
	Intensity    int
	Duration     time.Duration
	RateInterval time.Duration
	RateBurst    int
	AllowShock   bool
	AllowVibrate bool
	AllowedRooms []string
}

var defaultState = state{
	Paused:       true,
	Intensity:    0,
	Duration:     300 * time.Millisecond,
	RateInterval: 2 * time.Second,
	RateBurst:    2,
	AllowShock:   false,
	AllowVibrate: false,
	AllowedRooms: []string{},
}

const (
	MinIntensity = 0
	MaxIntensity = 100
	MinDuration  = 300 * time.Millisecond
	MaxDuration  = 30 * time.Second
)

func RestoreState() (*State, error) {
	basePath := stateDir()
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("cannot create state directory: %w", err)
	}

	statePath := filepath.Join(basePath, "state")

	value, err := persist.NewMustValueWithDefault(persist.CBORDriver, statePath, defaultState)
	if err != nil {
		return nil, err
	}

	state, _ := value.Load()

	return &State{
		Rate:  rate.NewLimiter(rate.Every(state.RateInterval), state.RateBurst),
		value: value,
	}, nil
}

func (s *State) Load() state {
	state, _ := s.value.Load()
	return state
}

func (s *State) Store(state state) {
	s.value.Store(state)
	s.Rate.SetBurst(state.RateBurst)
	s.Rate.SetLimit(rate.Every(state.RateInterval))
}

func stateDir() string {
	var statePath string
	if env := os.Getenv("STATE_DIRECTORY"); env != "" {
		slog.Debug(
			"state directory set by environment variable",
			"directory", env,
			"variable", "STATE_DIRECTORY")
		statePath = env
	} else {
		d, err := os.UserConfigDir()
		if err == nil {
			slog.Debug(
				"state directory set by user config directory",
				"directory", d,
				"function", "os.UserConfigDir")
		} else {
			slog.Debug(
				"state directory set to current working directory",
				"directory", ".")
		}
		statePath = filepath.Join(d, "shockhook")
	}
	return statePath
}
