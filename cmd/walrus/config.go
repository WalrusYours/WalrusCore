package main

import (
	"fmt"
	"net"
	"strings"
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
