// Datastar domain search demo: mock registry + artificial latency, streamed over one SSE response.
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"html/template"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/starfederation/datastar-go/datastar"
)

//go:embed templates/*.html
var tplFS embed.FS

//go:embed static
var staticFS embed.FS

var tpl = template.Must(template.New("").Funcs(template.FuncMap{
	"json": func(v any) (string, error) { b, err := json.Marshal(v); return string(b), err },
}).ParseFS(tplFS, "templates/*.html"))

var (
	topTLDs = []string{"com", "dev", "app", "io"}
	allTLDs = []string{"net", "org", "ai", "co", "xyz", "sh", "so", "site"}
	// Names that are always taken on .com, regardless of the hash.
	reservedCom = map[string]bool{"google": true, "vercel": true, "apple": true}
)

const maxQueryLen = 100 // raw text kept in the input / URL
const maxNameLen = 63   // DNS label limit

// ---- domain model -------------------------------------------------------

// Row is one domain line. Status is "" while the (fake) registry lookup is in flight.
type Row struct {
	ID, Name, Short, TLD, Status, Price string
}

// Results is everything the results area needs; rendered by SSR and by the SSE handler alike.
type Results struct {
	Name     string
	Top, All []Row
}

// Page is the full document model.
type Page struct {
	Q       string // raw query, prefilled into the input
	Results *Results
}

// sanitize turns free text ("My Cool Idea!", "Foo.com") into a DNS label ("mycoolidea", "foo").
func sanitize(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	if i := strings.IndexByte(q, '.'); i >= 0 { // user typed a TLD: keep the label only
		q = q[:i]
	}
	var b strings.Builder
	for _, r := range q {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > maxNameLen {
		s = strings.TrimRight(s[:maxNameLen], "-")
	}
	return s
}

func clampQuery(q string) string {
	q = strings.TrimSpace(q)
	if r := []rune(q); len(r) > maxQueryLen {
		q = string(r[:maxQueryLen])
	}
	return q
}

// truncateMiddle keeps the start and end of long names; the TLD is rendered separately so it never gets cut.
func truncateMiddle(s string, max int) string {
	if len(s) <= max {
		return s
	}
	head := (max - 1) / 2
	tail := max - 1 - head
	return s[:head] + "…" + s[len(s)-tail:]
}

func newRow(name, tld string) Row {
	return Row{ID: "row-" + tld, Name: name, Short: truncateMiddle(name, 22), TLD: tld}
}

func newResults(name string) *Results {
	if name == "" {
		return nil
	}
	r := &Results{Name: name}
	for _, t := range topTLDs {
		r.Top = append(r.Top, newRow(name, t))
	}
	for _, t := range allTLDs {
		r.All = append(r.All, newRow(name, t))
	}
	return r
}

// lookup is the deterministic mock registry: same name+tld => same answer, every time.
func lookup(row Row) Row {
	h := fnv.New32a()
	h.Write([]byte(row.Name + "." + row.TLD))
	v := h.Sum32()
	taken := v%100 < 35 || (row.TLD == "com" && (reservedCom[row.Name] || len(row.Name) <= 3))
	if taken {
		row.Status = "taken"
		return row
	}
	base := map[string]int{"com": 1125, "dev": 1500, "app": 1600, "io": 4600, "net": 1350, "org": 1000,
		"ai": 8000, "co": 2800, "xyz": 1200, "sh": 4800, "so": 3600, "site": 300}[row.TLD]
	cents := base + int(v>>8)%(base/2+1)
	row.Status = "available"
	row.Price = fmt.Sprintf("$%d.%02d", cents/100, cents%100)
	return row
}

// ---- rendering ------------------------------------------------------------

func render(name string, data any) string {
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
	return buf.String()
}

// ---- handlers -------------------------------------------------------------

// pageHandler server-renders the whole page. With ?q= the input is prefilled, the rows are already
// in the HTML (names are known without any lookup), and data-init kicks off the streaming search.
func pageHandler(w http.ResponseWriter, r *http.Request) {
	q := clampQuery(r.URL.Query().Get("q"))
	if r.URL.Path == "/domains" && q != "" {
		http.Redirect(w, r, "/domains/search?q="+url.QueryEscape(q), http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, "page", Page{Q: q, Results: newResults(sanitize(q))}); err != nil {
		log.Printf("page: %v", err)
	}
}

var activeSearches atomic.Int64

// searchHandler is the single SSE endpoint. One request = one search; Datastar aborts the fetch when a
// newer @get to the same URL starts, which cancels r.Context() here and stops every pending lookup.
func searchHandler(w http.ResponseWriter, r *http.Request) {
	var signals struct {
		Q string `json:"q"`
	}
	if err := datastar.ReadSignals(r, &signals); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := clampQuery(signals.Q)
	name := sanitize(q)

	sse := datastar.NewSSE(w, r)
	ctx := sse.Context()

	if name == "" { // the client doesn't request on empty input, but stay correct if it does
		sse.PatchElements(render("results", nil))
		sse.ReplaceURL(url.URL{Path: "/domains"})
		sse.PatchSignals([]byte(`{"_pending":false}`))
		return
	}

	// 1) Instantly: every row with a skeleton price, plus URL sync. Stable ids => morph, not re-create.
	res := newResults(name)
	sse.PatchElements(render("results", res))
	sse.ReplaceURL(url.URL{Path: "/domains/search", RawQuery: url.Values{"q": {q}}.Encode()})
	sse.PatchSignals([]byte(`{"_pending":false}`))

	// 2) Concurrently "ask the registry" for each TLD, patching only that row as it resolves.
	n := activeSearches.Add(1)
	defer activeSearches.Add(-1)
	start := time.Now()
	log.Printf("search %q start (active=%d)", name, n)

	rows := append(append([]Row{}, res.Top...), res.All...)
	var done atomic.Int32
	var wg sync.WaitGroup
	for _, row := range rows {
		wg.Add(1)
		go func(row Row) {
			defer wg.Done()
			delay := time.Duration(200+rand.IntN(1300)) * time.Millisecond // fake network latency
			select {
			case <-ctx.Done():
				return // client typed again / navigated away: stop the "remote" call
			case <-time.After(delay):
			}
			if err := sse.PatchElements(render("row", lookup(row))); err == nil {
				done.Add(1)
			}
		}(row)
	}
	wg.Wait()

	if ctx.Err() != nil {
		log.Printf("search %q CANCELLED after %v (%d/%d rows sent)", name, time.Since(start).Round(time.Millisecond), done.Load(), len(rows))
	} else {
		log.Printf("search %q done in %v (%d rows)", name, time.Since(start).Round(time.Millisecond), len(rows))
	}
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/domains", http.StatusFound)
	})
	mux.HandleFunc("GET /domains", pageHandler)
	mux.HandleFunc("GET /domains/search", pageHandler)
	mux.HandleFunc("GET /api/search", searchHandler)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	log.Printf("listening on http://localhost%s/domains", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
