# Output & the `--json` contract

The CLI has two audiences: humans at a terminal and scripts parsing output. It
serves both without compromise.

## Streams

- **stdout** is *data* — the one pipeable artifact of a command: a table, a JSON
  document, a raw message body, a one-time token, a stream of event frames.
- **stderr** is *everything else* — success/`✓` lines, warnings, prompts,
  progress, the "more results" hint, and the update notice.

They never intermix, so `openemail … > out` captures exactly the data and nothing
else, and a failed command still writes its data-or-nothing to stdout while the
error goes to stderr.

## `--json`

`--json` switches stdout to machine JSON. The shapes are a contract:

- **Single resources** are emitted as the object itself (`mailboxes get`,
  `messages get`, `domains get`, …).
- **Lists** are wrapped `{ "<plural>": [ … ], "nextCursor": "<cursor|"">" }`.
  `nextCursor` is **always present** (empty string on the last page). Pass it back
  with `--cursor`, or use `--all` to drain every page client-side.
- **Delivery & append results** (`send`, `deliver inbound`, `messages append`,
  `admin pickup ingest`) are echoed as **core's exact response bytes** — so union
  discriminators (`status`), `duplicate:false`, and null `uid`/`uidValidity`/
  `threadId` are preserved verbatim rather than lost to Go's `omitempty`.
- **Timestamps** are epoch **seconds** (as core emits them). Human output renders
  them in local time; `--json` keeps the raw number.
- **Booleans** are always real JSON `true`/`false` (never `0/1`).

Pagination exceptions (mirroring core):

- `labels messages` is a **UID window** (`--uid-min/--uid-max/--limit`), not a
  cursor. Page forward with `--uid-min <lastUid+1>`.
- `search --group-thread` is single-page (a `--cursor` there is rejected).
- `labels list`, `sieve scripts list`, and `credentials list` are unpaginated.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | a runtime/API error (rendered on stderr) |
| `2` | a usage error (bad flags/args; also non-TTY refusing to prompt) |
| `4` | authentication required / failed (`login` needed, or a 401) |

A few commands use the exit code as a signal: `sieve check` exits `1` when a
script does not compile (the report is still emitted), `deliver check` exits `1`
on a rejected recipient, and `api` exits `1` when the response status is ≥ 400.

The per-recipient send verbs — `compose` and `threads reply` — exit `1` when any
recipient failed, even though the submission itself succeeded (core answers
`207`). The full per-recipient report is still printed, so a script can notice
the failure from the exit code without parsing it. `messages restore` with
several ids does the same when any id came back `not_found`: the ones that
restored still did, and the table says which.

## Errors

Every error is core's `{ "error": "<snake_case_code>", … }` envelope, surfaced as
`error: <code> (HTTP <status>): <message>` on stderr, plus any validation issues
or extra fields, plus a compile `line:col` when present. A `404` adds the honesty
note that the resource may simply not be accessible with the current key — core
never distinguishes "missing" from "not yours" (no existence leaks).
