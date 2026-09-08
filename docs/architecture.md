# Architecture

This server is ports and adapters. The point is not the shape for its own sake:
it is that authentication and every tool are replaceable without touching
anything else, because a skeleton exists to be modified.

## The dependency rule

```
cmd/mcpskeleton/            main; serve, token and user commands
internal/app/               wiring — the ONLY package that imports adapters
internal/registry/          the one file you edit to add a tool
internal/core/              dispatch: authorize -> look up -> run -> classify
internal/port/              Authenticator, Authorizer, Tool, NoteStore
internal/domain/            Principal, Note, ToolError — stdlib only
internal/config/            environment -> validated struct
internal/adapter/in/http/   MCP over streamable HTTP, auth, probes, metadata
internal/adapter/out/       authenticator and note store implementations
internal/adapter/tool/      one package per tool
```

- `domain` imports nothing outside the standard library.
- `port` imports `domain`, and deliberately no MCP SDK types. A port that named
  the SDK would make the SDK unreplaceable, which is the failure mode ports
  exist to prevent.
- `core` imports `port` and `domain`.
- Adapters import `port` and `domain`.
- Only `app` imports adapters. Reading `app.Build` tells you the whole shape of
  the running system, and the choice of which adapter satisfies which port is
  made there and nowhere else.

## The ports

```go
type Authenticator interface {
    Authenticate(ctx context.Context, bearer string) (domain.Principal, error)
}

type Authorizer interface {
    Allow(p domain.Principal, requiredScope string) bool
}

type Tool interface {
    Name() string
    Description() string
    RequiredScope() string
    InputSchema() json.RawMessage
    Execute(ctx context.Context, p domain.Principal, args json.RawMessage) (Result, error)
}

type NoteStore interface {
    Add(ctx context.Context, owner, body string) (domain.Note, error)
    List(ctx context.Context, owner string) ([]domain.Note, error)
}
```

`Authenticator` is deliberately coarse. The adapter owns the entire decision of
what a credential means — parsing it, verifying it, and returning a complete
identity. A finer port such as `FindUserByTokenHash` would have avoided
duplicating verification logic across adapters, at the cost of breaking the day
an OIDC adapter arrives with no token hash to look up. The coarse port is the
one that survives the trajectory this codebase is on.

`Tool.Execute` takes the principal as a parameter rather than reading it from
the context. That makes "every tool receives the caller's identity" a property
the compiler enforces instead of a convention a new tool can forget.

`Tool.InputSchema` returns raw JSON so `port` needs no schema library. JSON
Schema is a wire format, so raw JSON is a faithful representation, and the MCP
SDK accepts `json.RawMessage` directly.

## The request path

```
HTTP request
  -> request logger (assigns an id, logs the outcome)
  -> bearer token extracted
  -> Authenticator.Authenticate    -- fails -> 401 + WWW-Authenticate -> metadata
  -> MCP dispatch
       tools/list -> filtered per request by the caller's scopes
       tools/call -> Authorizer.Allow -- no -> protocol error naming the scope
                  -> Tool.Execute(ctx, principal, args)
                       *domain.ToolError -> result with isError set
                       other error       -> protocol error, detail only to the log
```

Authentication runs **per request**, not once per session. That is what makes
revocation take effect on the next call rather than at the next reconnect, and
it is verified by a test that revokes a token on an open session.

`tools/list` is filtered by middleware on the result rather than by building a
per-caller server. Both filter the listing, but only this way stays routable, so
a forbidden call can be answered with the scope it needs instead of a misleading
"no such tool".

`TokenInfo.UserID` is set from the subject, which makes the transport itself
refuse a request that continues a session as a different user.

## Two error channels

A `*domain.ToolError` is a failure the model should read and may act on: bad
arguments, an empty body, a missing record. It becomes a result with `isError`
set, so the model can correct itself.

Everything else is an infrastructure failure the model cannot act on. It becomes
a protocol error, and its detail goes to the log rather than to the client.

Classifying in the core means every transport adapter maps the same way instead
of inventing its own convention.

## Authentication as it stands

The server is an OAuth 2.1 **resource server**: it validates bearer tokens,
publishes RFC 9728 protected-resource metadata, and answers unauthenticated
requests with `401` and a `WWW-Authenticate` header pointing at that document.

It is **not** an authorization server. It issues its own opaque tokens through
the CLI, so the metadata advertises no authorization server — which is the
honest answer, since there is nowhere to send a client that wants to start a
flow. Configure `MCPSKELETON_AUTHORIZATION_SERVERS` when that changes.

Tokens are `mcps_` followed by 32 bytes of `crypto/rand`, stored as SHA-256
hashes. SHA-256 rather than argon2id is deliberate: slow password hashes exist
to frustrate guessing a low-entropy human secret, and there is nothing to guess
against 256 bits of randomness. Argon2id would cost real latency on every
request to defend against an attack that cannot happen. If passwords are ever
added, that is when argon2id arrives.

Every rejection — absent, malformed, unknown, revoked, expired, disabled —
produces the identical message, so the response cannot be used to enumerate
which tokens exist. The reason travels on the error for logging and is
unreachable through `Error()`.

## Deliberately out of scope

Each of these is an adapter or a configuration field away. None is missing by
accident.

- **stdio transport.** There is no remote user over stdio; the OS user who
  launched the process is the user, which makes per-user authentication
  meaningless.
- **An embedded OAuth authorization server.** `/authorize`, `/token`, dynamic
  client registration, PKCE, consent and a login page is a second product in the
  same binary. Point at a real identity provider instead.
- **The database adapter.** The port and this note are the seam. Committing to a
  user schema now would be a decision made with the least information available.
- **Passwords and login flows.** This is a resource server. See above.
- **Native TLS.** A reverse proxy's job in essentially every real deployment.
- **OpenTelemetry.** Itself a driven adapter; adding it touches one file.
- **Runtime plugins.** Go's `plugin` package is brittle and adding a tool is
  meant to be a rebuild, which is what keeps the tool set compiler-checked.
- **Claude Desktop / claude.ai connectors.** Those drive the OAuth flow and
  cannot be given a static header, so they need the identity-provider path.

## Testing

Fakes are written at ports, never as mocks of concrete types.

- `internal/core` tests dispatch and authorization against fake tools.
- `notestest.RunContract` is the behaviour `NoteStore` promises, written once
  and run against both adapters, so "a tool cannot tell them apart" is checked
  rather than asserted.
- `internal/adapter/in/http` drives the real handler with the SDK's own client
  over a real socket, faking only the authenticator.
- `internal/app` acceptance tests fake nothing: real users file, real tokens,
  real tools. They cover revocation mid-session and survival across a restart.
