package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthURL(t *testing.T) {
	cases := map[string]string{
		"":               "http://127.0.0.1:8080/health",
		":8080":          "http://127.0.0.1:8080/health",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/health",
		"[::]:9000":      "http://127.0.0.1:9000/health",
		"127.0.0.1:8081": "http://127.0.0.1:8081/health",
		"garbage":        "http://127.0.0.1:8080/health",
	}
	for addr, want := range cases {
		got := healthURL(func(string) string { return addr })
		if got != want {
			t.Errorf("WALRUS_ADDR=%q: got %s, want %s", addr, got, want)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	status := http.StatusOK
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("probed %s, want /health", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer ts.Close()
	env := func(string) string { return strings.TrimPrefix(ts.URL, "http://") }

	if err := healthcheck(env); err != nil {
		t.Fatalf("healthy engine: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := healthcheck(env); err == nil {
		t.Fatal("a 503 must fail the check")
	}
	ts.Close()
	if err := healthcheck(env); err == nil {
		t.Fatal("an engine that is down must fail the check")
	}
}
