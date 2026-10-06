package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// healthURL is where `walrus healthcheck` probes: the engine's own listen address on loopback.
func healthURL(getenv func(string) string) string {
	addr := getenv("WALRUS_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://127.0.0.1:8080/health"
	}
	if host == "" || host == "0.0.0.0" || host == "::" || strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health"
}

// healthcheck is for the container HEALTHCHECK: the image is distroless, so there is no curl or
// wget to probe with. It returns nil when the running engine answers 200.
func healthcheck(getenv func(string) string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(healthURL(getenv))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned %s", resp.Status)
	}
	return nil
}
