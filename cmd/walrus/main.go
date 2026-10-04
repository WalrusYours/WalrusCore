// Command walrus is the WALRUS recommendation server.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/timurcravtov/walrus/internal/api"
	"github.com/timurcravtov/walrus/internal/rank"
	"github.com/timurcravtov/walrus/internal/schema"
	"github.com/timurcravtov/walrus/internal/store/memory"
)

const version = "0.0.0-dev"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// newInstanceID is random per boot for now; it should be generated once and kept in the
// store so it survives restarts.
func newInstanceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// wire picks the engine's parts: the in-memory store and the ranker that reads it.
func wire() api.Deps {
	st := memory.New()
	return api.Deps{Schema: schema.NewService(), Store: st, Ranker: rank.New(st)}
}

func main() {
	listen, err := resolveListen(os.Getenv)
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	if listen.DevDefault {
		slog.Warn("using the development admin key \"key\" on loopback only; set WALRUS_ADMIN_KEY for anything else")
	}

	cfg := api.Config{
		AdminKey:     listen.AdminKey,
		InstanceID:   env("WALRUS_INSTANCE_ID", newInstanceID()),
		InstanceName: env("WALRUS_INSTANCE_NAME", "walrus"),
		Version:      version,
	}
	srv := &http.Server{
		Addr:              listen.Addr,
		Handler:           api.New(cfg, wire()),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	slog.Info("walrus listening", "addr", listen.Addr, "instance_id", cfg.InstanceID, "instance_name", cfg.InstanceName)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
