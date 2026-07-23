package queue

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// Client wraps a River client backed by pgxpool.Pool.
type Client struct {
	River *river.Client[pgx.Tx]
}

// SetupRiverClient runs River schema migrations and initializes a River client with configured workers.
func SetupRiverClient(ctx context.Context, pool *pgxpool.Pool, workers *river.Workers) (*Client, error) {
	driver := riverpgxv5.New(pool)

	migrator, err := rivermigrate.New(driver, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create river migrator: %w", err)
	}

	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, &rivermigrate.MigrateOpts{})
	if err != nil {
		return nil, fmt.Errorf("failed to run river migrations: %w", err)
	}

	riverClient, err := river.NewClient(driver, &river.Config{
		Workers: workers,
		Queues: map[string]river.QueueConfig{
			"default": {MaxWorkers: 10},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create river client: %w", err)
	}

	if err := riverClient.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start river client: %w", err)
	}

	return &Client{River: riverClient}, nil
}

// Stop gracefully stops the River client worker loops.
func (c *Client) Stop(ctx context.Context) error {
	if c.River != nil {
		ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return c.River.Stop(ctxTimeout)
	}
	return nil
}
