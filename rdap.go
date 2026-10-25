package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// IANA bootstrap selects authoritative registry endpoints, avoiding a shared
// third-party redirect service's rate limit. Refresh periodically from IANA.
//
//go:embed rdap-bootstrap.json
var bootstrap []byte

type cachedStatus struct {
	status  string
	expires time.Time
}

type rdapRegistry struct {
	endpoints map[string]string
	client    *http.Client
	slots     chan struct{}
	mu        sync.Mutex
	cache     map[string]cachedStatus
	cooldown  map[string]time.Time
}

func newRegistry() *rdapRegistry {
	var data struct {
		Services [][][]string `json:"services"`
	}
	if err := json.Unmarshal(bootstrap, &data); err != nil {
		panic(err)
	}
	endpoints := make(map[string]string)
	for _, service := range data.Services {
		if len(service) != 2 {
			continue
		}
		for _, endpoint := range service[1] {
			if !strings.HasPrefix(endpoint, "https://") {
				continue
			}
			for _, tld := range service[0] {
				endpoints[tld] = strings.TrimRight(endpoint, "/")
			}
			break
		}
	}
	return &rdapRegistry{endpoints: endpoints, client: &http.Client{Timeout: 8 * time.Second}, slots: make(chan struct{}, 4), cache: make(map[string]cachedStatus), cooldown: make(map[string]time.Time)}
}

var registry = newRegistry()

func (registry *rdapRegistry) lookup(ctx context.Context, row Row) Row {
	row.Status = "unknown"
	endpoint, ok := registry.endpoints[row.TLD]
	if !ok {
		return row
	}
	domain := row.Name + "." + row.TLD
	registry.mu.Lock()
	cached, found := registry.cache[domain]
	registry.mu.Unlock()
	if found && time.Now().Before(cached.expires) {
		row.Status = cached.status
		return row
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case registry.slots <- struct{}{}:
		defer func() { <-registry.slots }()
	case <-ctx.Done():
		return row
	}
	registry.mu.Lock()
	blocked := time.Now().Before(registry.cooldown[endpoint])
	// Another request may have filled the cache while this one waited.
	cached, found = registry.cache[domain]
	registry.mu.Unlock()
	if found && time.Now().Before(cached.expires) {
		row.Status = cached.status
		return row
	}
	if blocked {
		return row
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/domain/"+domain, nil)
	if err != nil {
		return row
	}
	request.Header.Set("Accept", "application/rdap+json")
	request.Header.Set("User-Agent", "domain-search-datastar/1.0 (+https://domains.peterszarvas.hu)")
	response, err := registry.client.Do(request)
	if err != nil {
		return row
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		row.Status = "registered"
	case http.StatusNotFound:
		row.Status = "not-found"
	case http.StatusTooManyRequests:
		until := time.Now().Add(time.Minute)
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 && seconds <= 86400 {
			until = time.Now().Add(time.Duration(seconds) * time.Second)
		} else if date, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil && date.After(until) {
			until = date
		}
		registry.mu.Lock()
		registry.cooldown[endpoint] = until
		registry.mu.Unlock()
		return row
	default:
		return row // throttling, unsupported queries and outages are unknown
	}
	registry.mu.Lock()
	// Bound memory even when public visitors search many unique names.
	if len(registry.cache) >= 2048 {
		clear(registry.cache)
	}
	registry.cache[domain] = cachedStatus{row.Status, time.Now().Add(10 * time.Minute)}
	registry.mu.Unlock()
	return row
}
