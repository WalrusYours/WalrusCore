// Command walrus is the WALRUS recommendation server. Scaffold only: serves /health.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
)

const version = "0.0.0-scaffold"

// newInstanceID returns a random 128-bit id. Real implementation: generated once on first
// boot and persisted in the store, so it survives restarts (SERVER.md, instance identity).
func newInstanceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	// First-run admin comes from configuration, never from an open signup page.
	if os.Getenv("WALRUS_ADMIN_KEY") == "" {
		slog.Error("WALRUS_ADMIN_KEY is required: it bootstraps the first tenant and keys")
		os.Exit(1)
	}
	addr := env("WALRUS_ADDR", ":8080")
	id := env("WALRUS_INSTANCE_ID", newInstanceID())
	name := env("WALRUS_INSTANCE_NAME", "walrus")

	health := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":        "ok",
			"version":       version,
			"instance_id":   id,
			"instance_name": name,
		})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /v1/health", health)

	slog.Info("walrus listening", "addr", addr, "instance_id", id, "instance_name", name)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
