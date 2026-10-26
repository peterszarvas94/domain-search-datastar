package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

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
