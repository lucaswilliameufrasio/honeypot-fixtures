package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoadgenOnlyAcceptsLoopbackOrigin(t *testing.T) {
	if err := validate("https://example.com", "mixed", 1, time.Second, 1, 1); err == nil {
		t.Fatal("expected non-loopback target to be rejected")
	}
	if err := validate("http://127.0.0.1:8089", "mixed", 1, time.Second, 1, 1); err != nil {
		t.Fatalf("valid loopback target rejected: %v", err)
	}
	if err := validate("http://127.0.0.1:8089", "mixed", maxRate, time.Second, 1, 1); err != nil {
		t.Fatalf("maximum offered rate rejected: %v", err)
	}
	if err := validate("http://127.0.0.1:8089", "mixed", maxRate+1, time.Second, 1, 1); err == nil {
		t.Fatal("rate above the configured maximum should be rejected")
	}
	if err := validate("http://127.0.0.1:8089", "mixed", 1, time.Second, 4096, 8192); err != nil {
		t.Fatalf("maximum concurrency rejected: %v", err)
	}
	if err := validate("http://127.0.0.1:8089", "mixed", 1, time.Second, 4097, 8192); err == nil {
		t.Fatal("concurrency above the configured maximum should be rejected")
	}
}

func TestHealthModeUsesHealthEndpoint(t *testing.T) {
	if got, want := pathFor("health", 1), "/healthz"; got != want {
		t.Fatalf("health path = %q, want %q", got, want)
	}
}

func TestLoadgenReportsOfferedAndDeliveredRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__bench/echo" {
			t.Errorf("path = %q, want /__bench/echo", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var output strings.Builder
	if err := run(server.URL, "proxy", 10, time.Second, 4, 16, &output); err != nil {
		t.Fatal(err)
	}
	var got result
	if err := json.Unmarshal([]byte(output.String()), &got); err != nil {
		t.Fatalf("decode loadgen output: %v", err)
	}
	if got.Offered != 10 || got.Completed != 10 || got.Errors != 0 || got.Dropped != 0 {
		t.Fatalf("unexpected load result: %#v", got)
	}
	if got.ActualOfferedRPS != 10 {
		t.Fatalf("actual offered rate = %.1f, want 10", got.ActualOfferedRPS)
	}
}
