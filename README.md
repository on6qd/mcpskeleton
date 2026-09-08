# mcpskeleton

A working [Model Context Protocol](https://modelcontextprotocol.io) server in Go,
structured as ports and adapters.

Two things are adapters by design:

- **Authentication.** A local JSON users file today, a database later, an OIDC
  provider eventually. Swapping it is one package; the core never changes.
- **Every tool.** You customise this server by adding an adapter and one line to
  a registry.

Transport is streamable HTTP. Every request carries a bearer token, and every
tool receives the authenticated `Principal` as an explicit parameter — not
fished out of a context, so the compiler enforces it.

## Quickstart

```bash
make build

# Provision a user and mint them a credential. It is shown once.
./bin/mcpskeleton token issue --user alice --create \
    --scopes tools:echo,notes:read,notes:write --ttl 90d

./bin/mcpskeleton serve
```

Then point a client at it:

```bash
claude mcp add --transport http mcpskeleton http://localhost:8080/mcp \
    --header "Authorization: Bearer mcps_..."
```

Or drive it with the [MCP Inspector](https://github.com/modelcontextprotocol/inspector),
which has a field for the `Authorization` header:

```bash
npx @modelcontextprotocol/inspector
```

> The token ends up in plain text in your client's config (`~/.claude.json`, or a
> project `.mcp.json`). That is normal for bearer-token MCP servers, but do not
> commit `.mcp.json`. Use `--ttl` and `token revoke` accordingly.

## Managing credentials

```bash
mcpskeleton token issue --user alice --create --scopes notes:read --ttl 90d
mcpskeleton token list
mcpskeleton token revoke tok_1a2b3c
mcpskeleton user list
```

There is no signup and no login screen: users are provisioned by an operator.
`--create` is required to bring a new user into existence, so a typo fails
loudly instead of quietly creating a second account with a working credential.

Scopes belong to the token, not the user, so you can issue alice a read-only
token without touching the access her existing one grants.

Revocation takes effect on the very next request — authentication reads the
users file every time, with no cache to invalidate.

## The reference tools

| Tool | Scope | What it demonstrates |
|---|---|---|
| `echo` | `tools:echo` | The smallest complete tool: no dependencies. It returns the caller's subject, so you can confirm identity reaches the tool. |
| `notes_add` | `notes:write` | A tool with a driven port behind it. Notes are owned by the authenticated caller — the schema gives a caller nowhere to name anyone else. |
| `notes_list` | `notes:read` | A separate scope from writing, so a read-only token is possible. |

`notes` runs against either of two `NoteStore` adapters, chosen by
`MCPSKELETON_NOTES_STORE`, with nothing above the port aware of the difference.
That is the same swap authentication will make when it moves to a database.

## Adding a tool

1. Create `internal/adapter/tool/yours/` with an arguments struct and a function:

   ```go
   type Args struct {
       Thing string `json:"thing" jsonschema:"what to do it to"`
   }

   func New() port.Tool {
       return tool.Typed("yours", "Does the thing.", "tools:yours", run)
   }

   func run(ctx context.Context, p domain.Principal, in Args) (port.Result, error) {
       if in.Thing == "" {
           return port.Result{}, domain.NewToolError("thing must not be empty")
       }
       return port.Result{Text: "did it for " + p.Subject}, nil
   }
   ```

   `tool.Typed` derives the JSON Schema from your struct and validates every
   call against it, so a missing or misspelled argument is rejected before your
   function runs.

2. If it needs anything from outside, declare a port for it in `internal/port`
   and put the real implementation behind an adapter in
   `internal/adapter/out/`.

3. Add it to the slice in `internal/registry`.

4. Rebuild. Nobody can call it without the scope you gave it, and it will not
   appear in anyone's tool list without it either.

Return a `*domain.ToolError` for a failure the model should read and may retry
against; return any other error for an infrastructure failure it cannot act on.
The core routes the two differently and the model only ever sees the first.

## Swapping the authenticator

`port.Authenticator` is one method:

```go
Authenticate(ctx context.Context, bearer string) (domain.Principal, error)
```

The adapter owns the entire decision of what a credential means. To move to a
database, write `internal/adapter/out/auth/postgres` implementing that method
and change one line in `internal/app`. To move to an identity provider, write
one that verifies a JWT against a JWKS and maps claims to scopes, and set
`MCPSKELETON_AUTHORIZATION_SERVERS` so the published metadata points clients at
it.

Three constraints in the code exist to keep that true, and are worth preserving:

1. The authenticator returns a **complete** `Principal`. The core never looks
   anything up to finish filling it in.
2. Scope strings are **opaque to the core** — compared, never parsed. An
   external provider's scopes will not look like ours.
3. The protected-resource metadata is built from **configuration**, so pointing
   at a provider is not a code change.

## Configuration

All environment variables, read and validated once at boot.

| Variable | Default | Meaning |
|---|---|---|
| `MCPSKELETON_ADDR` | `:8080` | Listen address |
| `MCPSKELETON_BASE_URL` | `http://localhost:8080` | How clients reach this server. Behind a proxy this differs from `ADDR`, and it must be right: it is the resource identifier a token must be audienced to. |
| `MCPSKELETON_USERS_FILE` | `users.json` | Credentials file |
| `MCPSKELETON_NOTES_STORE` | `memory` | `memory` or `file` |
| `MCPSKELETON_NOTES_FILE` | `notes.json` | Used when `NOTES_STORE=file` |
| `MCPSKELETON_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `MCPSKELETON_AUTHORIZATION_SERVERS` | none | Comma-separated issuer URLs, published in the metadata document |

### Running behind a reverse proxy

Set `BASE_URL` to the public name. It carries a second consequence beyond the
metadata document: the MCP SDK refuses any request whose `Host` header is not
loopback while the listener is, which is DNS rebinding protection for a server
only this machine can reach. A proxy that terminates TLS and dials `127.0.0.1`
trips that check on every request, because the `Host` it forwards is the public
name. A non-loopback `BASE_URL` is how you say the check no longer describes
this deployment; leave it at the default and the protection stays on.

```caddyfile
mcp.example.com {
	reverse_proxy localhost:9100
}
```

```bash
MCPSKELETON_ADDR=:9100 MCPSKELETON_BASE_URL=https://mcp.example.com mcpskeleton serve
```

## Endpoints

| Path | Auth | Purpose |
|---|---|---|
| `/mcp` | Bearer token | The MCP endpoint. The only protected route. |
| `/.well-known/oauth-protected-resource` | Open | RFC 9728 metadata. A `401` points here so a client can discover where to authenticate. |
| `/healthz` | Open | Liveness. Checks nothing, deliberately. |
| `/readyz` | Open | Readiness. Fails if the users file or note store cannot be read. |

## Development

```bash
make check   # go vet + go test
make race    # tests under the race detector
make lint    # golangci-lint, if installed
make build
```

Design decisions and the build order are in
[docs/implementation-plan.md](docs/implementation-plan.md); the structure and
what is deliberately left out are in
[docs/architecture.md](docs/architecture.md).
