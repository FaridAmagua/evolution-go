# Transport recovery — local candidate, not deployed

This change keeps temporary websocket recovery in the pinned whatsmeow client,
instead of tearing down and recreating the Evolution instance from every
`Disconnected` handler. The existing client keeps its device, webhook, event
subscriptions and message handlers. Native retry backoff is cancelled by
`Disconnect`; `LoggedOut` stops recovery before persistence or webhook work.

An initial network failure retries only when a saved paired device is present.
Unpaired QR flows and deleted authentication stores do not auto-retry. Revoked
sessions still require the user to link from their phone. This does not recover
missing authentication records or move older same-phone device sessions.

The change also removes raw instance tokens from cache-clear log messages and
the full paired-instance log dump. Periodic presence starts only once per client
instead of multiplying background jobs after every reconnect.

## Local verification

Validated locally on 24 November 2026, based on main `dde1790`:

- `go test -race -count=1 ./pkg/whatsmeow/service` — passed.
- `go test -race -count=3 ./...` — passed, including the longer stop/backoff test.
- `go build -o /tmp/evolution-recovery-validated ./cmd/evolution-go` — passed.
- `git diff --check` — passed.

The full tests and build ran in Docker with `--network none`. Existing
PostgreSQL integration and PDF rasterization tests skip without their optional
database/tool dependencies; no live-provider integration was claimed.

The recovery tests use a fake HTTP transport returning connection-refused errors.
They do not connect to WhatsApp, use production credentials, send messages or
start paid searches. They cover paired/unpaired devices, initial outages, native
backoff, explicit stop/logout policy and deleted stores.

Before production: validate socket recovery with a dedicated test account.
Separately address the pre-existing shared map
accesses and recovery across a full process restart. Do not enable startup scans
of every disconnected instance without a persisted desired-state flag: that
would also restart deliberately stopped instances.

Pinned upstream contract:
https://github.com/tulir/whatsmeow/blob/b572e5bcb92b/client.go
