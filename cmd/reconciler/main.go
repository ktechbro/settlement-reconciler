package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ktechbro/settlement-reconciler/internal/reconcile"
)

func main() {
	if err := run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	var tokens map[string]reconcile.Principal
	if json.Unmarshal([]byte(os.Getenv("TENANT_TOKENS")), &tokens) != nil {
		return errors.New("TENANT_TOKENS must be a JSON token-to-principal map")
	}
	auth, err := reconcile.NewAuth(tokens)
	if err != nil {
		return err
	}
	var keys map[string]reconcile.TrustedKey
	if raw := os.Getenv("TRUSTED_KEYS"); raw != "" {
		if json.Unmarshal([]byte(raw), &keys) != nil {
			return errors.New("invalid TRUSTED_KEYS")
		}
	}
	fixtures := os.Getenv("ALLOW_FIXTURES") == "true"
	if !fixtures && len(keys) == 0 {
		return errors.New("configure TRUSTED_KEYS or explicitly enable ALLOW_FIXTURES for demo inputs")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	store, err := reconcile.Open(initCtx, os.Getenv("DATABASE_URL"))
	cancel()
	if err != nil {
		return err
	}
	defer store.Pool.Close()
	service := &reconcile.Service{Store: store, Verifier: reconcile.Verifier{Keys: keys, AllowFixtures: fixtures}}
	listen := os.Getenv("LISTEN_ADDR")
	if listen == "" {
		listen = "127.0.0.1:8081"
	}
	server := &http.Server{Addr: listen, Handler: reconcile.Handler(service, auth), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 25 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTicker(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				tickCtx, end := context.WithTimeout(ctx, 20*time.Second)
				err := service.Tick(tickCtx, auth.Tenants())
				end()
				if err != nil && ctx.Err() == nil {
					slog.Warn("deadline evaluation failed")
				}
			}
		}
	}()
	failures := make(chan error, 1)
	go func() { failures <- server.ListenAndServe() }()
	slog.Info("reconciler started", "listen", listen, "fixture_mode", fixtures)
	select {
	case <-ctx.Done():
	case err = <-failures:
		stop()
	}
	shutdown, end := context.WithTimeout(context.Background(), 10*time.Second)
	defer end()
	_ = server.Shutdown(shutdown)
	stop()
	<-done
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("HTTP server failed")
	}
	return nil
}
