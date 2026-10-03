package main

import "testing"

func fakeEnv(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestDefaultKeyIsLoopbackOnly(t *testing.T) {
	cfg, err := resolveListen(fakeEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminKey != "key" || !cfg.DevDefault || cfg.Addr != "127.0.0.1:8080" {
		t.Errorf("defaults = %+v", cfg)
	}
}

func TestDefaultKeyAllowsExplicitLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9000", "localhost:9000", "[::1]:9000"} {
		if _, err := resolveListen(fakeEnv(map[string]string{"WALRUS_ADDR": addr})); err != nil {
			t.Errorf("%s should be allowed: %v", addr, err)
		}
	}
}

func TestDefaultKeyIsRefusedOnReachableAddresses(t *testing.T) {
	for _, addr := range []string{":8080", "0.0.0.0:8080", "192.168.1.5:8080", "example.com:8080", "[::]:8080"} {
		if _, err := resolveListen(fakeEnv(map[string]string{"WALRUS_ADDR": addr})); err == nil {
			t.Errorf("default key on %s should be refused", addr)
		}
	}
}

func TestTypingTheDefaultKeyExplicitlyIsTreatedTheSame(t *testing.T) {
	_, err := resolveListen(fakeEnv(map[string]string{"WALRUS_ADMIN_KEY": "key", "WALRUS_ADDR": ":8080"}))
	if err == nil {
		t.Error("WALRUS_ADMIN_KEY=key on a public address should be refused")
	}
}

func TestRealKeyListensEverywhereByDefault(t *testing.T) {
	cfg, err := resolveListen(fakeEnv(map[string]string{"WALRUS_ADMIN_KEY": "a-long-random-key"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminKey != "a-long-random-key" || cfg.DevDefault || cfg.Addr != ":8080" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestRealKeyAllowsAnyAddress(t *testing.T) {
	cfg, err := resolveListen(fakeEnv(map[string]string{"WALRUS_ADMIN_KEY": "x1", "WALRUS_ADDR": "0.0.0.0:9000"}))
	if err != nil || cfg.Addr != "0.0.0.0:9000" {
		t.Errorf("cfg = %+v, err = %v", cfg, err)
	}
}
