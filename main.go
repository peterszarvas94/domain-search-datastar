// Datastar domain search demo: real RDAP registration lookups streamed over SSE.
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
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
	topTLDs   = []string{"com", "dev", "app", "io"}
	allTLDs   = []string{"net", "org", "ai", "co", "xyz", "sh", "so", "site"}
	extraTLDs = strings.Fields("online tech store shop cloud design studio digital agency solutions software systems network website space world today live life news media social video games devtools blog pro info biz me tv cc us uk de fr nl eu ca au ch at be it es pl cz se no fi dk in jp nz za club art ink app bio cafe center city click codes company computer consulting cool email events expert finance fun global group guide guru host hosting house land link ltd marketing mobi money name ninja one page photography photos pics press rocks services show tips tools top travel work works zone academy business capital chat community education engineering equipment exchange express family fashion financial fitness foundation fund gallery graphics health help international law legal management market partners plus production properties pub rentals restaurant reviews run sale school science security shoes shopping site soccer space support team technology training university ventures website wiki win wine yoga")
)

const maxQueryLen = 100 // raw text kept in the input / URL
const maxNameLen = 63   // DNS label limit

// ---- domain model -------------------------------------------------------

// Row is one domain line. Status is "" while the registry lookup is in flight.
type Row struct {
	ID, Name, Short, TLD, Status string
}

// Results is everything the results area needs; rendered by SSR and by the SSE handler alike.
type Results struct {
	Name      string
	Top, All  []Row
	HasMore   bool
	NextLimit int
}

func (r *Results) Count() int { return len(r.Top) + len(r.All) }

// Page is the full document model.
type Page struct {
	Q       string // raw query, prefilled into the input
	Results *Results
	Limit   int
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
	return Row{ID: "row-" + strings.ReplaceAll(tld, ".", "-"), Name: name, Short: truncateMiddle(name, 22), TLD: tld}
}

func newResults(query string) *Results {
	return resultsWithLimit(query, 12)
}

func resultsWithLimit(query string, limit int) *Results {
	if limit < 12 {
		limit = 12
	}
	if limit > 200 {
		limit = 200
	}
	name := sanitize(query)
	if name == "" {
		return nil
	}
	r := &Results{Name: name}
	// Keep a typed extension first, even when it is outside our suggestions.
	_, suffix, exact := strings.Cut(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(query)), "."), ".")
	if exact {
		for _, label := range strings.Split(suffix, ".") {
			if label == "" || sanitize(label) != label {
				exact = false
				break
			}
		}
	}
	if exact {
		r.Top = append(r.Top, newRow(name, suffix))
	}
	for _, t := range topTLDs {
		if exact && t == suffix {
			continue
		}
		r.Top = append(r.Top, newRow(name, t))
	}
	for _, t := range allTLDs {
		if exact && t == suffix {
			continue
		}
		r.All = append(r.All, newRow(name, t))
	}
	seen := make(map[string]bool)
	for _, row := range append(append([]Row{}, r.Top...), r.All...) {
		seen[row.TLD] = true
	}
	for _, t := range extraTLDs {
		if seen[t] {
			continue
		}
		if _, supported := registry.endpoints[t]; !supported {
			continue
		}
		seen[t] = true
		r.All = append(r.All, newRow(name, t))
	}
	if keep := limit - len(r.Top); keep < len(r.All) {
		r.All = r.All[:keep]
		r.HasMore = true
		r.NextLimit = limit + 12
	}
	return r
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
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 12 || limit > 200 {
		limit = 12
	}
	if err := tpl.ExecuteTemplate(w, "page", Page{Q: q, Results: resultsWithLimit(q, limit), Limit: limit}); err != nil {
		log.Printf("page: %v", err)
	}
}

var activeSearches atomic.Int64

// searchHandler is the single SSE endpoint. One request = one search; Datastar aborts the fetch when a
// newer @get to the same URL starts, which cancels r.Context() here and stops every pending lookup.
func searchHandler(w http.ResponseWriter, r *http.Request) {
	var signals struct {
		Q      string `json:"q"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}
	if err := datastar.ReadSignals(r, &signals); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := clampQuery(signals.Q)
	name := sanitize(q)
	res := resultsWithLimit(q, signals.Limit)
	if signals.Offset != 0 && (res == nil || signals.Offset < 12 || signals.Offset%12 != 0 ||
		signals.Limit != signals.Offset+12 || signals.Limit > 200 || signals.Offset >= res.Count()) {
		http.Error(w, "invalid expansion range", http.StatusBadRequest)
		return
	}

	sse := datastar.NewSSE(w, r)
	ctx := sse.Context()

	if name == "" { // the client doesn't request on empty input, but stay correct if it does
		sse.PatchElements(render("results", nil))
		sse.ReplaceURL(url.URL{Path: "/domains"})
		sse.PatchSignals([]byte(`{"_pending":false}`))
		return
	}

	rows := append(append([]Row{}, res.Top...), res.All...)
	if signals.Offset == 0 {
		// A new search replaces the results; an expansion leaves earlier rows untouched.
		sse.PatchElements(render("results", res))
	} else {
		rows = rows[signals.Offset:]
		// Remove only this batch before appending it, so reconnects cannot duplicate rows.
		selectors := make([]string, 0, len(rows))
		for _, row := range rows {
			selectors = append(selectors, "#"+row.ID)
		}
		sse.PatchElements("", datastar.WithSelector(strings.Join(selectors, ",")), datastar.WithMode(datastar.ElementPatchModeRemove))
		sse.PatchElements(render("rows", rows), datastar.WithSelector("#all-results"), datastar.WithMode(datastar.ElementPatchModeAppend))
		sse.PatchElements(render("show-more", res))
	}
	values := url.Values{"q": {q}}
	if signals.Limit > 12 && signals.Limit <= 200 {
		values.Set("limit", strconv.Itoa(signals.Limit))
	}
	sse.ReplaceURL(url.URL{Path: "/domains/search", RawQuery: values.Encode()})
	sse.PatchSignals([]byte(`{"_pending":false}`))

	// 2) Concurrently query RDAP for each TLD, patching only that row as it resolves.
	n := activeSearches.Add(1)
	defer activeSearches.Add(-1)
	start := time.Now()
	log.Printf("search %q start (active=%d)", name, n)

	var done atomic.Int32
	var wg sync.WaitGroup
	for _, row := range rows {
		wg.Add(1)
		go func(row Row) {
			defer wg.Done()
			result := registry.lookup(ctx, row)
			if ctx.Err() != nil {
				return
			}
			if err := sse.PatchElements(render("row", result)); err == nil {
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
