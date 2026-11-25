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

## Persisted restart intent

`instances.resume_on_startup` is a nullable, server-owned boolean added by the
existing GORM migration. It is not exposed or writable through public JSON.

- Explicit connect/reconnect enables it; manual disconnect/logout disables it.
- A temporary socket error updates observed status without erasing intent.
- A revoked session disables restoration.
- Legacy NULL rows resume only if their previous observed status was connected.
- The first eligible legacy restore persists explicit opt-in before its worker
  starts, so a later outage followed by a process restart does not lose intent.
- Startup filters by client name and requires a stored device JID. It restores
  only that exact saved device, never an older same-phone device or a new QR.
- Authentication database errors remain distinct from missing/revoked sessions.
- Offline disconnect does not start a client and preserves webhook/subscriptions.
- Offline logout stops recovery but does not claim remote unlink was confirmed.

PostgreSQL checks include nullable migration of existing rows, isolation between
client names, intent surviving a fresh repository/connection, a real restart of
the disposable PostgreSQL container (before/after probe), and the existing shared
authentication-pool concurrency integration test. All use synthetic fixtures.

Reproduce with `EVOGO_RECOVERY_TEST_DSN` pointing only to the disposable
`evogo_recovery_test` database. The test refuses other database names and hosts.
Run `TestStartupIntentAcrossActualPostgresRestart` with
`EVOGO_RECOVERY_RESTART_PHASE=before`, restart only that test PostgreSQL, then run
with `EVOGO_RECOVERY_RESTART_PHASE=after`. The after phase removes its own schema.
The ordinary tests use temporary unique schemas and clean up automatically.

## Attempt ownership (25 November, local only)

- Reserve one owner synchronously before launching any asynchronous start.
  Duplicate starts return a conflict, without replacing the stop signal.
- Capture each owner's cancellation context/signal; old QR workers cannot look
  up or stop a replacement by instance ID. QR waits are interruptible.
- Retain ownership until native callbacks and QR workers have drained. Map
  cleanup removes only the pointer/channel owned by that attempt.
- Protect every production access to the shared client maps. Message sends hold
  one client reference instead of repeatedly looking up a changing map slot.
- Serialize incompatible HTTP actions per instance with rejection (409), not a
  queue. Other instances/channels remain independent.
- A read/send request may restore only an opted-in exact saved session. It cannot
  undo a manual disconnect or open an unsolicited QR pairing.
- QR expiry never calls remote Logout. A PairSuccess racing cancellation still
  persists the exact device JID without re-enabling recovery or claiming the
  cancelled socket is connected. Teardown no longer fabricates LoggedOut.

Tests cover concurrent starts, stop-before-publication, duplicate actions before
DB writes, independent channels, stale timeout signals, cancellable QR rotation,
pair success racing cancellation, and preservation of manual-stop intent.

Final local candidate: `go test -race -count=3 ./...`, full binary build and
`git diff --check` passed on 25 November. Tests/build used a network-isolated
Docker container with no production credentials. Optional PostgreSQL/PDF tests
remain conditional; the earlier dedicated PostgreSQL checks are described above.

Before production: validate socket recovery with a dedicated test account.
These tests do not prove all native network interleavings or webhook delivery
ordering. Runtime settings/global device-property writes are a separate legacy
concurrency concern, not a guarantee supplied by map-slot locking. Do not infer
from a passing race detector on these scenarios that every runtime interleaving
has been covered. Back up production before the additive migration; rollback to
an older binary can leave the unused nullable column in place.

The personal production device was observed connected with its exact auth record
on 25 November at 09:42 UTC. No production session was disconnected, replaced or
restarted while developing this candidate. Deploying the provider needs a planned
restart; do not restart the currently working account merely to test this patch.

Pinned upstream contract:
https://github.com/tulir/whatsmeow/blob/b572e5bcb92b/client.go
