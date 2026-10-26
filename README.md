# Domain search · Datastar demo

Live domain registration lookups using public RDAP registry services, streamed
row by row with Datastar. No API key, prices, simulated results or fake latency.

```bash
go run . -addr :8080
# Open http://localhost:8080/domains
go test -race ./...
```

The server uses the committed IANA RDAP bootstrap to query authoritative HTTPS
registry endpoints. A 200 response means **Registered**, 404 means **Not found**,
and unsupported extensions, timeouts or errors show **Unknown**. Not found is
not a guarantee of purchase availability: reserved and blocked names may still
be unavailable. No registration or purchasing is performed.

Results stream over one SSE response, and new input cancels previous requests.
The shared in-memory cache holds up to 2,048 results for ten minutes. At most four
registry requests run concurrently across visitors. A 429 response pauses queries
to that registry for at least a minute (honoring Retry-After). These safeguards
reduce traffic; they do not guarantee that registries will never rate limit us.
Caches reset on restart. Queries are sent to the corresponding registry.

Refresh the bootstrap occasionally:

```bash
curl -fsS https://data.iana.org/rdap/dns.json -o rdap-bootstrap.json
```

## Deployment

Build and run the included Docker image:

```bash
docker build -t domain-search-demo .
docker run --rm -p 8080:8080 domain-search-demo
# Open http://localhost:8080/domains
```

The container listens on port 8080; `/domains` can be used as a health check.
No persistent storage or environment variables are required. When deploying
publicly, put the service behind an HTTPS reverse proxy.

An exact domain such as `example.computer` appears first, followed by
alternative extensions. Typed extensions are preserved even outside the default
suggestion list. Multi-label suffixes such as `.co.uk` use the final TLD's RDAP
service. Unsupported registries show Unknown rather than omitting the domain.

More than 80 extensions can be browsed in batches of 12 with Show more. Changing
the search resets to the first batch. Expanding preserves the existing rows and
streams lookups only for the newly added extensions. UI labels are Available
(RDAP 404), Taken (RDAP 200), and Unknown; Available remains subject to
confirmation by a registrar.
