package main

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/timurcravtov/walrus/internal/schema"
)

const devAdminKey = "key"

type listenConfig struct {
	AdminKey   string
	Addr       string
	DevDefault bool
}

// resolveListen picks the admin key and listen address from the environment. Without
// WALRUS_ADMIN_KEY it falls back to the development key and loopback only; that key is
// never allowed on an address other machines can reach.
func resolveListen(getenv func(string) string) (listenConfig, error) {
	key := getenv("WALRUS_ADMIN_KEY")
	addr := getenv("WALRUS_ADDR")
	cfg := listenConfig{AdminKey: key, Addr: addr}

	if key == "" {
		cfg.AdminKey = devAdminKey
		if addr == "" {
			cfg.Addr = "127.0.0.1:8080"
		}
	} else if addr == "" {
		cfg.Addr = ":8080"
	}
	cfg.DevDefault = cfg.AdminKey == devAdminKey

	if cfg.DevDefault && !isLoopback(cfg.Addr) {
		return cfg, fmt.Errorf("the default admin key %q may only be used on a loopback address, but WALRUS_ADDR is %q: set WALRUS_ADMIN_KEY", devAdminKey, cfg.Addr)
	}
	return cfg, nil
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// minTrainInterval keeps a typo from retraining the models in a loop.
const minTrainInterval = time.Minute

// resolveTrainInterval reads WALRUS_TRAIN_INTERVAL, how often the engine retrains its learned models
// (a schema duration such as 15m or 1d). Unset or 0 means only when asked, with POST /v1/models/train.
func resolveTrainInterval(getenv func(string) string) (time.Duration, error) {
	raw := getenv("WALRUS_TRAIN_INTERVAL")
	if raw == "0" {
		return 0, nil
	}
	d, err := schema.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("WALRUS_TRAIN_INTERVAL: %w", err)
	}
	if d != 0 && d.Std() < minTrainInterval {
		return 0, fmt.Errorf("WALRUS_TRAIN_INTERVAL is %q; use %s or more, or leave it unset to train only on request", raw, minTrainInterval)
	}
	return d.Std(), nil
}
