package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistryResponsesAndCache(t *testing.T) {
	for _, test := range []struct {
		code   int
		status string
	}{{200, "registered"}, {404, "not-found"}, {429, "unknown"}, {500, "unknown"}} {
		t.Run(test.status+http.StatusText(test.code), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/domain/example.com" {
					t.Errorf("path %q", r.URL.Path)
				}
				w.WriteHeader(test.code)
			}))
			defer server.Close()
			registry := newRegistry()
			registry.endpoints = map[string]string{"com": server.URL}
			row := newRow("example", "com")
			if got := registry.lookup(context.Background(), row).Status; got != test.status {
				t.Fatalf("got %s", got)
			}
			registry.lookup(context.Background(), row)
			want := int32(1)
			if test.code == 500 {
				want = 2
			}
			if calls.Load() != want {
				t.Fatalf("upstream calls=%d, want %d", calls.Load(), want)
			}
			if test.code == 429 {
				registry.lookup(context.Background(), newRow("different", "com"))
				if calls.Load() != 1 {
					t.Fatal("cooldown did not cover other domains")
				}
			}
		})
	}
}

func TestRegistryCancellationAndUnsupported(t *testing.T) {
	registry := newRegistry()
	if got := registry.lookup(context.Background(), newRow("example", "invalid")).Status; got != "unknown" {
		t.Fatal(got)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	registry.endpoints = map[string]string{"com": server.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got := registry.lookup(ctx, newRow("example", "com")).Status; got != "unknown" {
		t.Fatal(got)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation was not propagated")
	}
}

func TestExpiredCacheIsRefreshed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer server.Close()
	registry := newRegistry()
	registry.endpoints = map[string]string{"com": server.URL}
	registry.cache["example.com"] = cachedStatus{"registered", time.Now().Add(-time.Minute)}
	if got := registry.lookup(context.Background(), newRow("example", "com")).Status; got != "not-found" {
		t.Fatal(got)
	}
}
