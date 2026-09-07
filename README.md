# mcpskeleton

A working [Model Context Protocol](https://modelcontextprotocol.io) server in Go,
structured as ports and adapters.

Two things are adapters by design:

- **Authentication** — a local JSON users file today, a database later, an OIDC provider
  eventually. Swapping it is one package; the core never changes.
- **Every tool** — customise the server by adding an adapter package and one registry
  line.

Transport is streamable HTTP. Every request carries a bearer token, every tool receives
the authenticated `Principal` as an explicit parameter.

## Status

Under construction. See [docs/implementation-plan.md](docs/implementation-plan.md) for
the design decisions and the build order.

## Development

```
make check     # go vet + go test
make build     # build the binary
make race      # tests under the race detector
```
