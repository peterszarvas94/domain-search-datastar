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

Coolify on hub builds the Dockerfile from `master` through the shared Forgejo
bridge. Push deployments serve https://domains.peterszarvas.hu/domains on
container port 8080; `/domains` is the health check. No persistent data or
required environment variables.

An exact domain such as `peterszarvas.computer` appears first, followed by
alternative extensions. Typed extensions are preserved even outside the default
suggestion list. Multi-label suffixes such as `.co.uk` use the final TLD's RDAP
service. Unsupported registries show Unknown rather than omitting the domain.

More than 80 extensions can be browsed in batches of 12 with Show more. Changing
the search resets to the first batch. Successful earlier lookups come from the
shared cache when expanding. UI labels are Available (RDAP 404), Taken (RDAP
200), and Unknown; Available remains subject to confirmation by a registrar.
