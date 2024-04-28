package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"

	"github.com/caarlos0/env/v11"
	"github.com/google/uuid"
	"libdb.so/hserve"
)

type Config struct {
	OpenshockAPIToken  string     `env:"OPENSHOCK_API_TOKEN,required"`  // get from website
	OpenshockAPIServer string     `env:"OPENSHOCK_API_SERVER,required"` // https://api.shocklink.net
	OpenshockShockerID configUUID `env:"OPENSHOCK_SHOCKER_ID,required"` // get from website
	ShockhookAddr      string     `env:"SHOCKHOOK_ADDR,required"`       // :8080
	ShockhookSecret    string     `env:"SHOCKHOOK_SECRET,required"`     // POST /command/{secret}
}

type configUUID uuid.UUID

func (c *configUUID) UnmarshalText(text []byte) error {
	u, err := uuid.Parse(string(text))
	if err != nil {
		return err
	}
	*c = configUUID(u)
	return nil
}

func (c configUUID) AsUUID() uuid.UUID {
	return uuid.UUID(c)
}

func main() {
	logger := slog.Default()
	slog.SetDefault(logger)

	config, err := env.ParseAs[Config]()
	if err != nil {
		logger.Error(
			"cannot parse environment variables",
			"err", err)
		os.Exit(1)
	}

	state, err := RestoreState()
	if err != nil {
		logger.Error(
			"cannot restore state",
			"err", err)
		os.Exit(1)
	}

	handler, err := newHandler(config, state)
	if err != nil {
		logger.Error(
			"cannot create server",
			"err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	logger.Info(
		"server starting",
		"addr", config.ShockhookAddr)

	if err := hserve.ListenAndServe(ctx, config.ShockhookAddr, handler); err != nil {
		logger.Error(
			"server failed",
			"err", err)
		os.Exit(1)
	}
}
