package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"sessionProject/internal/auth"
	"sessionProject/internal/config"
	"sessionProject/internal/database"
	"sessionProject/internal/httpapi"
	"sessionProject/internal/store"
	"sessionProject/migrations"
)

func New(ctx context.Context, cfg *config.Config) (*http.Server, func(), error) {
	pool, err := database.NewPostgresPool(ctx, cfg.DB)
	if err != nil {
		return nil, nil, err
	}
	migrationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = migrations.Apply(migrationCtx, pool)
	cancel()
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	rdb, err := database.NewRedisClient(ctx, cfg.Redis)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	cleanup := func() { _ = rdb.Close(); pool.Close() }
	service, err := auth.NewService(store.NewUsers(pool), store.NewSessions(rdb), cfg.Session.TTL)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	ready := func(ctx context.Context) error { return errors.Join(pool.Ping(ctx), rdb.Ping(ctx).Err()) }
	return &http.Server{
		Addr:              ":" + cfg.AppPort,
		Handler:           httpapi.New(service, store.NewLimiter(rdb), cfg.Session, ready),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}, cleanup, nil
}
