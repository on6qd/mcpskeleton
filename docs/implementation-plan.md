# Implementation Plan — mcpskeleton

A working MCP server in Go, structured as ports-and-adapters, where authentication is an
adapter (local file today, database later, IdP eventually) and every tool is an adapter
added by writing one package and one registry line.

This document is the frozen direction. It is the output of a design interview; the
decisions in it were made deliberately and should not be quietly revised during
implementation. If a step turns out to be wrong, stop and raise it.

---

## 1. Decisions

| # | Decision | Choice |
|---|---|---|
| 1 | Toolchain | Go, latest stable, installed via Homebrew |
| 2 | Protocol layer | Official `github.com/modelcontextprotocol/go-sdk` |
| 3 | Transport | Streamable HTTP only — no stdio adapter |
| 4 | Identity propagation | `Principal` is an explicit parameter on `Execute` |
| 5 | Authorization | Thin central scope check: filters `tools/list`, rejects `tools/call` |
| 6 | Credential | Opaque bearer token, self-managed; server acts as an OAuth 2.1 Resource Server (RFC 9728 metadata + correct `401`) but is not itself an authorization server |
| 7 | Auth seam | One coarse `Authenticator` port; all verification logic lives in the adapter |
| 8 | When auth runs | Per request, plus a check that the subject matches the session's original subject |
| 9 | Layout | Ports-and-adapters under `internal/`; tools live in-repo |
| 10 | Tool port | Raw JSON at the port + a generic `tool.Typed` helper for ergonomics |
| 11 | Config | Env vars into a validated struct + stdlib `flag` for CLI. No config library |
| 12 | Tool errors | Typed domain error -> `isError` result; anything else -> protocol error; auth -> HTTP `401`/`403` |
| 13 | User store | JSON file; SHA-256 token hashes + constant-time compare; no passwords |
| 14 | CLI | `token issue --create` / `token list` / `token revoke` / `user list`; atomic file writes |
| 15 | Reference tools | `echo` (pure) + `notes` (behind `NoteStore` with two adapters: memory, JSON file) |
| 16 | Acceptance gate | Unit tests with fakes + in-process end-to-end test driving the real server with the SDK client |
| 17 | Observability | `log/slog` JSON; subject, tool, duration, outcome; tokens never logged |
| 18 | DB adapter | Not written — port + documented seam only |
| 19 | Furniture | README, Makefile, GitHub Actions, `.golangci.yml`, `docs/architecture.md` |
| 20 | Target client | Claude Code / MCP Inspector / custom clients |

### Swap-proofing constraints (non-negotiable, enforced in review)

1. `Authenticator` returns a **fully-formed** `Principal` — subject, scopes, expiry. The
   core never looks anything up to complete it.
2. Scope strings are **opaque to the core** — it compares them, never parses or
   enumerates them.
3. The protected-resource-metadata document is **built from config**, with an empty
   authorization-server list today.

These exist so that replacing the local authenticator with an OIDC one is a new adapter
and nothing else.

---

## 2. Architecture

```
cmd/mcpskeleton/            main; serve + token/user subcommands
internal/domain/            Principal, ToolError, Note — stdlib imports only
internal/port/              Authenticator, Authorizer, Tool, NoteStore
internal/core/              dispatch: authz check -> tool lookup -> execute -> map errors
internal/config/            env -> struct, validated at boot
internal/adapter/in/http/   streamable-HTTP MCP server, auth middleware,
                            healthz/readyz, protected-resource metadata
internal/adapter/out/auth/local/    JSON users-file authenticator
internal/adapter/out/notes/memory/  in-memory NoteStore
internal/adapter/out/notes/file/    JSON-file NoteStore
internal/adapter/tool/echo/         one package per tool
internal/adapter/tool/notes/
internal/registry/          the one file edited to add a tool
```

Dependency rule: `domain` imports nothing outside stdlib; `port` imports `domain`;
`core` imports `port` + `domain`; adapters import `port` + `domain`; only `cmd` imports
adapters.

### Ports

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
    InputSchema() *jsonschema.Schema
    Execute(ctx context.Context, p domain.Principal, args json.RawMessage) (Result, error)
}

type NoteStore interface {
    Add(ctx context.Context, owner string, body string) (domain.Note, error)
    List(ctx context.Context, owner string) ([]domain.Note, error)
}
```

### Request path

```
HTTP request
  -> extract Authorization: Bearer ...
  -> Authenticator.Authenticate   -- fails -> 401 + WWW-Authenticate -> /.well-known/oauth-protected-resource
  -> subject matches session's?   -- no    -> 401
  -> MCP dispatch (SDK)
       tools/list -> filtered by Authorizer.Allow
       tools/call -> Authorizer.Allow -- no -> 403
                  -> Tool.Execute(ctx, principal, args)
                       domain.ToolError -> CallToolResult{isError: true}
                       other error      -> JSON-RPC protocol error
```

---

## 2b. SDK findings (v1.7.0), and what they change

Inspected after the plan was written. The SDK already provides infrastructure the plan
assumed we would write:

- `auth.RequireBearerToken(verifier, opts)` — HTTP middleware that verifies a bearer
  token, puts the result in the request context, and on failure returns `401` with a
  `WWW-Authenticate` header pointing at the protected-resource metadata.
- `auth.ProtectedResourceMetadataHandler(metadata)` — serves the RFC 9728 document.
- `auth.TokenInfo.UserID` — when a `TokenVerifier` sets it, the streamable-HTTP
  transport itself ensures every request in a session comes from the same user. Session
  hijacking prevention is therefore transport-level, not ours.
- `auth.TokenInfoFromContext(ctx)` — how an authenticated identity reaches a handler.
- `mcp.AddTool` derives input/output JSON Schema from Go types by reflection, and
  validates incoming arguments.

Consequences for the steps below:

- **E2** becomes a thin bridge: a `auth.TokenVerifier` closure that calls our
  `port.Authenticator` and maps `domain.Principal` -> `*auth.TokenInfo`. The SDK type
  never crosses into `core` or `domain`; the port stays ours, which is what keeps the
  OIDC swap a single-adapter change.
- **E3** becomes constructing the metadata struct from config and handing it to the
  SDK handler.
- **E5** becomes setting `TokenInfo.UserID` from `Principal.Subject` and an integration
  test asserting the transport rejects a swapped token — not hand-written pinning logic.
- **C1**'s `tool.Typed` still exists: the core dispatches on our own `port.Tool`, and the
  MCP adapter registers a single generic bridge per tool. The SDK's reflection is used
  for schema generation, not as a substitute for the port.

None of the section 1 decisions change.

---

## 3. Working agreement

- Smallest possible increments. One step = one coherent change.
- Every step ships with unit or integration tests where testable, and every step ends in
  a commit. No step is left in a non-building state.
- `go build ./... && go vet ./... && go test ./...` must pass before each commit.
- Tests use fakes at ports, never mocks of concrete types.
- No step introduces a third-party dependency other than the MCP SDK without raising it
  first.

---

## 4. Steps

Each step is independently committable. Dependencies are noted where they exist.

### Phase A — Foundation

**A1. Repo bootstrap.**
`go.mod` (module `github.com/bartdelepeleer/mcpskeleton`), `.gitignore`, `Makefile`
(build/test/vet/lint/run), README stub, `docs/` already present.
_Test:_ `go build ./...` succeeds on an empty tree. No unit tests yet.

**A2. Domain types.**
`internal/domain`: `Principal` (Subject, Scopes, ExpiresAt), `Note`, `ToolError` and the
sentinel errors (`ErrUnauthenticated`, `ErrForbidden`, `ErrToolNotFound`).
_Test:_ construction, `ToolError` wrapping/unwrapping, `errors.Is` behaviour.

**A3. Ports.**
`internal/port`: the four interfaces plus `Result`. No implementations.
_Test:_ compile-time assertions only; real coverage arrives with implementations.

### Phase B — Core

**B1. Scope authorizer.**
`internal/core`: the default `Authorizer` — exact-match scope comparison, opaque strings,
empty required scope means "no scope needed".
_Test:_ table test — allow, deny, empty requirement, empty principal scopes.

**B2. Tool registry.**
`internal/core`: register tools, look up by name, list. Duplicate name is a programming
error and panics at wiring time.
_Test:_ register/lookup/list, duplicate detection, unknown name.

**B3. Dispatch service.**
`internal/core`: `ListTools(principal)` filtered by the authorizer; `CallTool(ctx,
principal, name, args)` doing authz -> lookup -> execute -> error classification.
_Test:_ fakes for `Tool` and `Authorizer`; covers filtered list, forbidden call, unknown
tool, tool error vs infrastructure error.

### Phase C — Tools

**C1. Typed tool helper.**
`internal/adapter/tool`: `Typed[In, Out](name, desc, scope, fn) port.Tool` — schema
derived from `In` by reflection, args unmarshalled, unmarshal failure becomes a
`ToolError`.
_Test:_ schema generation for a sample struct, happy path, malformed args, propagated
domain error.

**C2. `echo` tool.**
`internal/adapter/tool/echo`, scope `tools:echo`.
_Test:_ execute returns the input; registered metadata is correct.

**C3. `NoteStore` memory adapter.**
`internal/adapter/out/notes/memory` — per-owner isolation, concurrency-safe.
_Test:_ add/list, owner isolation, concurrent access under `-race`.

**C4. `NoteStore` file adapter.**
`internal/adapter/out/notes/file` — JSON file, atomic write (temp + rename), created on
first use.
_Test:_ round-trip across instances, atomicity of write, corrupt-file handling. Shared
contract test run against both adapters.

**C5. `notes` tool.**
`internal/adapter/tool/notes` — `notes_add` (scope `notes:write`) and `notes_list`
(scope `notes:read`), notes owned by `Principal.Subject`.
_Test:_ against the memory adapter; asserts ownership comes from the principal, not args.

### Phase D — Authentication

**D1. Token primitives.**
`internal/adapter/out/auth/local`: generate `mcps_<base64url(32 random bytes)>`, SHA-256
hash, constant-time compare.
_Test:_ format, uniqueness, hash stability, compare correctness.

**D2. Users file store.**
Read/write the JSON users file; atomic write; create, find by token hash, revoke, list.
_Test:_ round-trip, missing file, malformed file, atomic replace, revoked token absent.

**D3. Local authenticator adapter.**
Implements `port.Authenticator`: strip prefix, hash, look up, check enabled + expiry,
return a fully-formed `Principal`.
_Test:_ valid token, unknown token, disabled user, expired token, malformed header.

### Phase E — HTTP

**E1. Config.**
`internal/config`: env -> struct (bind address, users file path, note store kind and
path, log level, external base URL, authorization-server list), validated at boot.
_Test:_ defaults, overrides, validation failures.

**E2. Auth middleware.**
`internal/adapter/in/http`: a `auth.TokenVerifier` bridging to `port.Authenticator`,
mapping `domain.Principal` -> `*auth.TokenInfo` (including `UserID`), wrapped with
`auth.RequireBearerToken`.
_Test:_ `httptest` — missing header, malformed header, bad token, success, and that the
`401` carries a `WWW-Authenticate` pointing at the metadata document.

**E3. Operational endpoints.**
`/healthz`, `/readyz`, and `/.well-known/oauth-protected-resource` served by
`auth.ProtectedResourceMetadataHandler` from a config-built metadata struct.
_Test:_ `httptest` — status codes, metadata document shape, config-driven AS list.

**E4. MCP server adapter.**
Wire the SDK's streamable-HTTP handler to the core service: `tools/list` filtered,
`tools/call` dispatched with the principal from context.
_Test:_ in-process integration test using the SDK's own client — initialize, list, call.

**E5. Session subject pinning.**
Set `TokenInfo.UserID` from `Principal.Subject` so the transport pins a session to one
user. This is verification of SDK behaviour rather than new logic.
_Test:_ integration — open a session with one token, reuse the session id with a
different user's token, expect rejection.

### Phase F — Binary

**F1. `serve` command.**
`cmd/mcpskeleton`: wiring, `slog` JSON logger, graceful shutdown on SIGTERM/SIGINT.
_Test:_ smoke test — boot on a random port, hit `/healthz`, shut down cleanly.

**F2. Structured request logging.**
Request id, subject, tool name, duration, outcome. Tokens never logged.
_Test:_ capture `slog` output, assert fields present and token absent.

**F3. Token CLI.**
`token issue --user --create --scopes --ttl`, `token list`, `token revoke`, `user list`.
_Test:_ against a temp users file — issue creates, issue without `--create` on unknown
user fails, revoke invalidates, list output shape.

### Phase G — Closing

**G1. End-to-end acceptance test.**
One test that boots the real binary's wiring and drives it with the SDK client: happy
path, `401` unauthenticated, `403` wrong scope, `401` revoked token, `isError` from a
failing tool.

**G2. Documentation and CI.**
README (quickstart, how to add a tool, how to connect Claude Code, how to swap the
authenticator), `docs/architecture.md` (the hexagon, the ports, what is deliberately out
of scope), `.golangci.yml`, GitHub Actions running build/vet/test/lint.

---

## 5. Out of scope

stdio transport; an embedded OAuth authorization server; the Postgres adapter; passwords
and login flows; native TLS (reverse proxy's job); OpenTelemetry; runtime plugins;
Claude Desktop / claude.ai connector support.

Each is an adapter or a config field away. `docs/architecture.md` records why.

---

## 6. Verification the agent cannot do alone

The automated gate (unit + in-process end-to-end) runs unattended. Verifying against a
real client needs the user: MCP Inspector requires Node, and `claude mcp add` writes to
the user's own config. Both commands are documented in the README for a joint session at
the end.

---

## 7. Implementation record

Every step in section 4 is built, tested and committed. Deviations from the plan
as written, and why:

- **Ports do not import a schema library.** `Tool.InputSchema` returns
  `json.RawMessage` rather than `*jsonschema.Schema`, which would have pulled the
  SDK's schema package into `port` and through it into `core`. The MCP SDK
  accepts `json.RawMessage` for a tool's schema, so this costs nothing.
- **`tool.Typed` has no `Out` type parameter.** `port.Tool` carries no output
  schema, so one would have constrained `Result.Structured` and nothing else,
  while forcing every prose-only tool to name a type it does not have.
- **`tool.Typed` validates against the schema.** Not in the plan, and necessary:
  the SDK's low-level tool registration leaves validation to the caller, so
  without it a published `required` field was silently accepted as a zero value.
- **Scopes live on the token, not the user.** It makes issuing a read-only token
  possible without touching existing access, and matches how OAuth providers
  work, so the OIDC adapter will not change the shape of a `Principal`.
- **`AuthFailure` lives in `domain`, not in the local adapter.** A rejection that
  reveals nothing while carrying a loggable reason is part of the
  `Authenticator` contract, not one adapter's detail.
- **One MCP server, not one per session.** The plan implied a per-session server
  for filtering. That made unlisted tools unroutable, so a caller lacking a scope
  was told "no such tool" — contradicting the decision to give a clear message.
  `tools/list` is now filtered by middleware per request instead, which also
  makes the listing reflect the token presented now rather than at session open.
- **E5 needed no code.** Setting `TokenInfo.UserID` is enough; the transport
  pins a session to one user. Verified by a test that first replays the identical
  request as the session's owner, so it cannot pass for another reason.

Not done, because it cannot be done unattended: verification against a real
client. The README documents both `claude mcp add` and MCP Inspector. Everything
else is covered by the automated suite, including end-to-end acceptance tests
that fake nothing.
