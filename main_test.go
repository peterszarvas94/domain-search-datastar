package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func searchResponse(t *testing.T, query string, limit, offset int) *httptest.ResponseRecorder {
	t.Helper()
	signals, err := json.Marshal(map[string]any{"q": query, "limit": limit, "offset": offset})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/api/search?datastar="+url.QueryEscape(string(signals)), nil)
	response := httptest.NewRecorder()
	searchHandler(response, request)
	return response
}

func TestShowMoreOnlyStreamsNewRows(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()
	original := registry
	registry = newRegistry()
	defer func() { registry = original }()
	for tld := range registry.endpoints {
		registry.endpoints[tld] = upstream.URL
	}
	for _, tld := range append(append([]string{}, topTLDs...), allTLDs...) {
		registry.endpoints[tld] = upstream.URL
	}

	for _, query := range []string{"example", "example.computer", "example.co.uk"} {
		t.Run(query, func(t *testing.T) {
			finalOffset := (resultsWithLimit(query, 200).Count() - 1) / 12 * 12
			for _, batch := range []struct{ limit, offset int }{{12, 0}, {24, 12}, {36, 24}, {24, 12}, {finalOffset + 12, finalOffset}, {12, 0}} {
				// Clear the cache to prove that earlier rows never reach lookup at all.
				clear(registry.cache)
				paths = nil
				response := searchResponse(t, query, batch.limit, batch.offset)
				if response.Code != http.StatusOK {
					t.Fatalf("status %d: %s", response.Code, response.Body.String())
				}
				body := response.Body.String()
				res := resultsWithLimit(query, batch.limit)
				rows := append(append([]Row{}, res.Top...), res.All...)
				if len(paths) != len(rows)-batch.offset {
					t.Fatalf("upstream calls=%d, want %d", len(paths), len(rows)-batch.offset)
				}
				for i, row := range rows {
					present := strings.Contains(body, `id="`+row.ID+`"`)
					if present != (i >= batch.offset) {
						t.Fatalf("offset %d: unexpected patch presence for %s", batch.offset, row.ID)
					}
					path := "/domain/" + row.Name + "." + row.TLD
					count := 0
					for _, got := range paths {
						if got == path {
							count++
						}
					}
					want := 0
					if i >= batch.offset {
						want = 1
					}
					if count != want {
						t.Fatalf("lookup %s: got %d calls, want %d", path, count, want)
					}
				}
				if batch.offset > 0 {
					if strings.Contains(body, `id="results"`) || !strings.Contains(body, "selector #all-results\ndata: mode append") || !strings.Contains(body, "data: mode remove") {
						t.Fatal("expansion must append a retry-safe batch without replacing results")
					}
				}
				if !res.HasMore && !strings.Contains(body, `data-show="false"`) {
					t.Fatal("final batch must hide the pagination button")
				}
			}
		})
	}
}

func TestInvalidExpansionRange(t *testing.T) {
	for _, test := range []struct{ limit, offset int }{{24, -1}, {24, 1}, {24, 24}, {36, 12}, {212, 200}, {12, 999999}} {
		if got := searchResponse(t, "example", test.limit, test.offset).Code; got != http.StatusBadRequest {
			t.Errorf("%+v: got %d", test, got)
		}
	}
	if got := searchResponse(t, "", 24, 12).Code; got != http.StatusBadRequest {
		t.Errorf("empty expansion: got %d", got)
	}
}

func TestExactDomainComesFirst(t *testing.T) {
	for _, query := range []string{"example.computer", "Example.COM", "example.xyz", "example.co.uk"} {
		t.Run(query, func(t *testing.T) {
			results := newResults(query)
			first := results.Top[0]
			if got := first.Name + "." + first.TLD; got != strings.ToLower(query) {
				t.Fatalf("first=%s", got)
			}
			seen := map[string]bool{}
			for _, row := range append(results.Top, results.All...) {
				if seen[row.ID] {
					t.Fatalf("duplicate %s", row.ID)
				}
				seen[row.ID] = true
			}
			request := httptest.NewRequest("GET", "/domains/search?q="+query, nil)
			response := httptest.NewRecorder()
			pageHandler(response, request)
			if !strings.Contains(response.Body.String(), `title="`+strings.ToLower(query)+`"`) {
				t.Fatal("SSR lost exact query")
			}
		})
	}
}

func TestSearchSuggestionsStillWork(t *testing.T) {
	for _, query := range []string{"My Cool Idea", "example.", "example.<script>", "example.bad/extension"} {
		results := newResults(query)
		if len(results.Top) != 4 || len(results.All) != 8 {
			t.Fatalf("unexpected rows for %q", query)
		}
	}
}

func TestExpandedExtensions(t *testing.T) {
	first := resultsWithLimit("example.computer", 12)
	more := resultsWithLimit("example.computer", 24)
	all := resultsWithLimit("example.computer", 200)
	if len(first.Top)+len(first.All) != 12 || len(more.Top)+len(more.All) != 24 {
		t.Fatal("batch size ignored")
	}
	if !first.HasMore || first.NextLimit != 24 || all.HasMore {
		t.Fatal("pagination state incorrect")
	}
	if len(all.Top)+len(all.All) < 80 {
		t.Fatal("not enough extensions")
	}
	for _, results := range []*Results{first, more, all} {
		if results.Top[0].TLD != "computer" {
			t.Fatal("exact extension lost")
		}
	}
}
