// Command walrus is the WALRUS recommendation server. Scaffold only: serves /health.
package main

import (
	"log/slog"
	"net/http"
	"os"
)

func main() {
	addr := os.Getenv("WALRUS_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	slog.Info("walrus listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
