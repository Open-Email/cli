# Live black-box e2e suite (drives the CLI against a DEPLOYED core)

The CLI analogue of openemail-core's `test/live` suite. It builds the real
`openemail` binary and drives it end-to-end over HTTPS against a **deployed**
openemail-core worker, asserting on what the CLI actually surfaces — exit codes,
the `--json` payloads on stdout, and the machine error codes it prints to stderr
— rather than raw HTTP (which core's own live suite covers).

Behind the `live` build tag, so a plain `go test ./...` (and `make test` /
`make integration`) never touches a live backend.

## Run

```sh
OE_HOST=https://<your-worker-host> \
OE_SYSTEM_KEY=oek_...system-role-token... \
make live
# or: go test -tags live ./test/...
```

The suite **skips itself** (green, no cases run) when `OE_HOST` / `OE_SYSTEM_KEY`
are unset — safe to leave wired into any pipeline. The env vars are the same
names core's live suite uses, so one environment runs both.

### Environment

| Var                  | Required | Default               | Purpose                                            |
| -------------------- | :------: | --------------------- | -------------------------------------------------- |
| `OE_HOST`            |    yes   | —                     | Worker origin, e.g. `https://oe.acme.workers.dev`  |
| `OE_SYSTEM_KEY`      |    yes   | —                     | `role='system'` `oek_` bearer (provisioning + admin) |
| `OE_D1_DATABASE`     |    no    | `openemail-directory` | D1 database name used by teardown                  |
| `OE_SKIP_D1_CLEANUP` |    no    | —                     | `1` to skip the wrangler D1 sweep (leaves residue) |

Provisioning runs with the **system key** — accounts create, cross-tenant reads,
admin, and the send-disabled outbound case all require it, exactly as core's
harness does. `OE_HOST` is passed to the binary as `OPENEMAIL_API_URL`; each
process gets an isolated `XDG_CONFIG_HOME` and `OPENEMAIL_NO_KEYRING=1`, so no
real profile or keychain is touched.

Teardown's D1 sweep needs `wrangler` (found on `PATH`, else `npx wrangler`)
authenticated against the account that owns the DB. Without it the API-phase
teardown still runs; only the account-row/tombstone cleanup is skipped (a warning
prints).

## Test files

Each file provisions its own throwaway resources tagged with a per-run id
(`cli<base36-ts><rand>`) on throwaway `.test` domains, reused lazily across the
cases in that file, and everything is torn down once in `TestMain` after the run.

| File | Covers | Mirrors (core) |
| --- | --- | --- |
| `e2e_live_test.go` | health, inbound+raw, verify-login, duplicate delivery, outbound Sent-dedup, group fan-out, search+reindex, trash→purge, restore, mailbox soft-delete/restore, domain traffic | `e2e.live.test.ts` |
| `routing_live_test.go` | routing ladder — pattern glob, exact-beats-pattern, tag-stripped, bare-`*` catch-all (paced), domain-alias rewrite | `routing.live.test.ts` |
| `authz_live_test.go` | mailbox-principal directory fence (`insufficient_scope`), cross-tenant `not_found` no-leak (+ system-key control), account-key domain confinement | `authz.live.test.ts` |
| `delivery_errors_live_test.go` | delivery error mappings — `over_quota`, `receiving_disabled` (×2), `unknown_address`, `sending_disabled` | `delivery-errors.live.test.ts` |
| `labels_live_test.go` | label CRUD + system-label protection, per-label UID listing, label EXPUNGE | `labels.live.test.ts` |
| `messages_crud_live_test.go` | APPEND (+ invalid flag), PATCH STORE/COPY/MOVE + last-label expunge, DELETE `--label` and `--purge` | `messages-crud.live.test.ts` |

The `harness_test.go` file holds `TestMain` (build + teardown), the binary
runners, the provisioning helpers, `poll`, `mime`, and the JSON accessors.

## CLI-specific divergences from core's assertions

Where core asserts a raw HTTP status + error string, the CLI turns a 4xx/5xx into
a non-zero exit and prints the machine error code; each case asserts that instead.
Two inputs the CLI rejects **client-side** never reach core, so they surface as a
usage exit rather than the server error core sees:

- `messages append --flags <bogus>` → the CLI's flag validation (not core's
  `invalid_flag`).
- `messages delete --purge --label <x>` → the CLI's mutual-exclusion guard (not
  core's `label_purge_exclusive`).

## What it can't assert (same as core)

The out-of-band checks the API can't observe are noted with `[NOTE]` log lines,
not asserted: relay receipt of an outbound message, physical R2 blob removal after
purge, the Iceberg traffic-sink flush, and the 7-day mailbox wipe.

## Teardown & known residue

1. **API phase** — soft-delete every mailbox created (arming each DO's deadman +
   closing admission, the only path that schedules the deferred store/blob wipe),
   and remove group routes.
2. **D1 phase** — `wrangler d1 execute --remote` removes the run's account rows,
   mailbox tombstones, and any orphaned directory rows (scoped by `account_id`).

Unavoidable via the API/D1 (documented, not a leak): a soft-deleted mailbox's DO
SQLite + R2 blobs self-wipe only after the 7-day undo window — there is no
synchronous force-wipe, so test mailbox stores persist (unreachable) until then.
If a run **crashes** before teardown, its rows are tagged `e2e-live <runId>` and
its domains are `<runId>-N.test`; sweep leftovers by hand with the same
`DELETE ... WHERE account_id=...` shape used in `harness_test.go`.
