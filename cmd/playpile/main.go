// Command playpile runs the playpile web server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"playpile/internal/catalog"
	"playpile/internal/steam"
	"playpile/internal/store"
	"playpile/internal/web"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("playpile failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	addr := env("PLAYPILE_ADDR", ":8080")
	dataDir := env("PLAYPILE_DATA_DIR", "./data")
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("PLAYPILE_BASE_URL")), "/")
	apiKey := strings.TrimSpace(os.Getenv("PLAYPILE_STEAM_API_KEY"))
	devLogin := truthy(os.Getenv("PLAYPILE_DEV_LOGIN"))

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return err
	}

	st, err := store.Open(dataDir)
	if err != nil {
		return err
	}
	defer st.Close()

	client := steam.New(apiKey)
	if v := os.Getenv("PLAYPILE_STEAM_STORE_BASE"); v != "" {
		client.StoreBase = v
	}
	if v := os.Getenv("PLAYPILE_STEAM_API_BASE"); v != "" {
		client.APIBase = v
	}

	cat := catalog.New(st, client)

	var steamAPI web.SteamAPI
	if apiKey != "" {
		steamAPI = steamAdapter{client}
	} else {
		log.Warn("PLAYPILE_STEAM_API_KEY is not set: logins will have placeholder names and no library import")
	}

	srv, err := web.New(web.Config{
		BaseURL:         baseURL,
		DevLogin:        devLogin,
		SteamAPIKey:     apiKey,
		DefaultCountry:  env("PLAYPILE_DEFAULT_COUNTRY", "US"),
		DefaultLanguage: env("PLAYPILE_DEFAULT_LANGUAGE", "english"),
		Version:         version,
	}, st, cat, steamAPI, log)
	if err != nil {
		return err
	}

	if baseURL == "" {
		log.Warn("PLAYPILE_BASE_URL is not set: falling back to the request's host for the Steam login realm")
	}
	if devLogin {
		log.Warn("PLAYPILE_DEV_LOGIN is enabled: anyone can log in without Steam")
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Housekeeping: expired sessions leave rows behind otherwise.
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := st.PurgeExpiredSessions(); err != nil {
					log.Warn("purge sessions", "err", err)
				}
			case <-stop:
				return
			}
		}
	}()
	defer close(stop)

	errCh := make(chan error, 1)
	go func() {
		log.Info("playpile listening", "addr", addr, "data", filepath.Clean(dataDir), "version", version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-signals:
		log.Info("shutting down", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return httpServer.Shutdown(ctx)
}

// steamAdapter adapts the Steam client to the interface the web layer wants.
type steamAdapter struct{ c *steam.Client }

// PlayerSummary fetches a public profile.
func (a steamAdapter) PlayerSummary(ctx context.Context, steamID string) (web.Player, error) {
	p, err := a.c.PlayerSummary(ctx, steamID)
	if err != nil {
		return web.Player{}, err
	}
	return web.Player{SteamID: p.SteamID, PersonaName: p.PersonaName, Avatar: p.Avatar, ProfileURL: p.ProfileURL}, nil
}

// OwnedGames lists the app ids in a public library.
func (a steamAdapter) OwnedGames(ctx context.Context, steamID string) ([]int, error) {
	return a.c.OwnedGames(ctx, steamID)
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
