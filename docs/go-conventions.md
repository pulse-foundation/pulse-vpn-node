# Go conventions for Pulse VPN Node

These are implementation defaults. No Go toolchain, module, migration tool, or CI commands exist
in the repository yet; add runnable commands together with their implementation.

## Code and ownership

- Use `gofmt`. Prefer ordinary functions and methods, small responsibility-based packages, concrete
  constructors, and small consumer-defined interfaces. Do not transfer the Pulse TypeScript
  conventions for arrow functions, monorepo packages, or framework layering into Go.
- Return useful wrapped errors where callers need their cause (`%w`, `errors.Is`/`errors.As`).
  Classify errors for HTTP responses without leaking internal configuration. Log an error once at
  its handling boundary. Reserve panic for violated programmer invariants, not bad user input.
- Pass request/operation contexts explicitly, normally as the first argument. Do not replace them
  with `context.Background()` inside I/O paths. Background workers have an application-owned lifetime,
  with a fresh bounded context for each attempt; request cancellation must not erase durable intent.
- Define who owns each goroutine, child process, connection, transaction, and timer. Bound I/O and
  shutdown, reap children, close resources on partial startup failure, and avoid detached retries.
- Prefer immutable snapshots or clear synchronization for revision state. Do not hold a global mutex
  or database transaction across a network call. Never copy structs containing mutexes after use.
- Use `crypto/rand` for credentials and UUID randomness. Keep secrets out of URLs, logs, traces,
  panic messages, test output, image layers, and routine API responses. Credential issuance/export
  requires a dedicated authenticated response and must not pass through request/response logging.

## External boundaries

- HTTP handlers authenticate, validate bounded request bodies, call a service, and map outcomes.
  Set server/client timeouts and gracefully shut down. Middleware does not own reconciliation.
- Start the core with an executable and argument list, not interpolated shell commands. Capture
  bounded, sanitized diagnostics. Cancellation must not accidentally kill a healthy shared child
  when an individual HTTP request ends.
- Use parameterized SQL, bounded contexts, explicit transactions and checked commit errors.
  Within a transaction, execute all participating queries through that transaction, not the pool.
  Let unique constraints and conditional writes enforce persistence invariants under concurrency.
- Configure dependencies once at startup. Test seams should represent real boundaries such as clock,
  core, and database, rather than an interface for every struct. Keep production code free of test
  flags and sleep overrides added only to make assertions easy.

## Tests and checks

Use table-driven tests when cases share behavior, `httptest` for HTTP, `t.TempDir` and `t.Cleanup`
for isolation, and focused fakes only at real external boundaries. Use an actual disposable
PostgreSQL for transaction/migration semantics and the pinned core for reload/connection behavior.
Wait on conditions with deadlines rather than fixed sleeps. Test outcomes and failure recovery,
not private helper calls or source-text patterns. Run subprocess failure tests without exposing secrets.

Once a Go module exists, the baseline is formatting, `go vet ./...`, `go test ./...`, and
`go build ./cmd/pulse-vpn-node`. Run `go test -race ./...` for concurrency/lifecycle changes and in
supported CI; provision its toolchain requirements rather than silently skipping it. Integration
tests must have an explicit documented command and clean up only their own resources.

Pin and configure Staticcheck and `govulncheck` when quality tooling is added. Avoid piling on several
overlapping linters or recording commands as implemented before scripts/CI actually exist. Use the
smallest relevant checks during iteration and complete the project gates before a release. A docs-only
change needs link/frontmatter/diff checks, not pretend application test results.

## Authoritative references

- [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments): naming, interfaces, errors,
  contexts and goroutine ownership.
- [Executing transactions](https://go.dev/doc/database/execute-transactions): transaction boundaries;
  the same boundary discipline applies when using native pgx APIs.
- [Race detector](https://go.dev/doc/articles/race_detector): runtime race checking.
- [Go security practices](https://go.dev/doc/security/best-practices): dependency checks and tooling.

Consult these for language questions rather than adding copied style manuals to local skills.
