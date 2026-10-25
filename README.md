# Datastar domain search (Vercel-style), mock data + fake latency

A recreation of the Vercel `/domains` search box built with **Datastar v1.0.4** (CDN), **Go 1.24+ `net/http`**
and the official **Go SDK `github.com/starfederation/datastar-go` v1.2.2**. There's no build step and no JS of my own.
The "registry" is a deterministic hash, and every TLD answers after a random 200–1500 ms, so it looks like a remote lookup.

```bash
go run . -addr :8080      # open http://localhost:8080/domains
# or: go build -o domain-search . && ./domain-search -addr :8080
```

## How it works

| Step | Who | What |
|---|---|---|
| keystroke | browser | `data-on:input` sets `$_pending` (rows fall back to skeletons) |
| 300 ms idle | browser | `data-on:input__debounce.300ms` fires `@get('/api/search')`; signal `q` is sent automatically |
| t=0 | server | patches the `#results` section with all 12 rows (names are known right away), calls `replaceState` to `/domains/search?q=…`, and sets `_pending=false` |
| t=200–1500 ms | server | one goroutine per TLD sleeps for its fake latency, then patches **only** `#row-<tld>` (`Available $x/yr` or `Taken`) on the **same** SSE response |
| new keystroke | browser | Datastar's default `requestCancellation: 'auto'` aborts the previous `GET /api/search`, so `r.Context()` is cancelled and every pending goroutine returns |
| empty input | browser | no request. CSS shows "Begin your search" (driven by `$q`) and the URL is reset to `/domains` |

Loading `/domains/search?q=foo` directly **server-renders** the prefilled input and all the skeleton rows.
A `data-init="@get('/api/search')"` then starts the stream. The same templates (`results`, `row`) serve the SSR page and the SSE
patches, so there's no hydration step and the two renderings can't drift apart.

## Best practices applied

- **Server is the source of truth.** Sanitizing, availability, prices, URL state and markup are all decided in Go. The client only holds the input value.
- **Backend-driven UI over SSE.** The server sends `datastar-patch-elements` and `datastar-patch-signals` events and the browser morphs them in.
- **Few signals.** `q` (bound to the input) plus the local `_pending` and `_searching` indicator. Signals starting with `_` aren't sent to the server.
- **Stable element ids** (`#results`, `#row-com`, …) so Datastar morphs rows in place instead of re-creating them.
- **Debounce lives in the attribute** (`__debounce.300ms`), not in custom JS.
- **Cancellation through context.** It relies on Datastar's built-in request cancellation, and the handler `select`s on `ctx.Done()` (see the `CANCELLED` lines in the server log).
- **SSR for the initial state.** The page works and is shareable with `?q=`. Without JS, the form still submits as a plain GET.
- **One SSE response per search.** Rows are written concurrently, which is safe because the SDK's generator serializes writes with a mutex.

## Mock rules

- `fnv32(name + "." + tld)` decides availability (about 35% taken) and the price inside a per-TLD range, so the same query always gives the same answers.
- `google`, `vercel` and `apple` (and any name of 3 characters or fewer) are always taken on `.com`.
- Sanitizing: trim, lowercase, drop anything typed after the first `.`, keep only `[a-z0-9-]`, trim hyphens, cap at 63 characters. Free text like "my cool idea" becomes `mycoolidea`.
- Middle truncation happens on the server (`thisisaver…startupidea`). The TLD is a separate span, so it's never cut. The full name is in `title`.

## Files

`main.go` (handlers and mock), `templates/page.html` (page, results and row templates), `static/style.css`, `screenshots/`.
