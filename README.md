# openemail

The command-line client for the [OpenEmail](https://open.email) platform — in the
style of the Stripe CLI and wrangler. Authenticate once; the CLI mints its own
dedicated account API key, stores it in your OS keychain, and uses it for every
command. It covers the **entire** core API: the directory plane (accounts,
domains, routes, patterns, credentials), the mailbox data plane (messages,
labels, threads, search, sieve, pickups), and the workflow verbs (`send`,
`watch`, `deliver`, `api`, operator `admin`).

## Install

```sh
# Homebrew (macOS / Linux)
brew install Open-Email/tap/openemail
# Homebrew 6+ gates third-party taps — if prompted, trust it once:
#   brew trust --tap open-email/tap

# Debian / Ubuntu / Fedora — download the .deb/.rpm from Releases, then:
sudo dpkg -i openemail_*.deb      # or: sudo rpm -i openemail_*.rpm

# Windows — download the .zip from Releases and put openemail.exe on your PATH.

# Go
go install github.com/Open-Email/cli/cmd/openemail@latest

# From source
git clone https://github.com/Open-Email/cli && cd cli
make build && ./bin/openemail version
```

Shell completions ship in the release archives (bash/zsh/fish) and are
installed automatically by brew/apt/dnf. Generate them yourself with
`openemail completion <shell>` (also supports powershell).

## Quick start

```sh
# Log in — paste an account key (oek_…). The CLI mints its own per-device key,
# stores it in the OS keychain, records the profile, and discards the pasted key.
openemail login

# Non-interactive / CI:
OPENEMAIL_API_KEY=oek_… openemail login --api-key oek_…

openemail whoami         # who this key resolves to
openemail status         # health + auth probe

# Pick a default mailbox so message/label/sieve commands don't need -m each time.
openemail mailboxes use alice@example.com   # accepts an address or a ULID
```

## Command map

Every mailbox-scoped group takes `-m/--mailbox <id|address>` (an address is
resolved via its route); set a per-profile default with `openemail mailboxes use`.

| Group | What it does |
|---|---|
| `login` / `logout` / `whoami` / `status` | auth lifecycle and identity |
| `ui` (alias `console`) | full-screen console: sidebar + tables for mailboxes/domains/routes/patterns/keys/accounts with create/edit forms, confirm-gated deletes, and group-member editing; drill into a mailbox for a live message list (events WebSocket), compose-and-send, previews, flag toggles, label editing, and a trash view with restore |
| `mailboxes` | create/list/get/update/delete/restore, `use` (set default) |
| `keys` | account API keys: create/list/revoke |
| `accounts` | accounts (create/list are system-only), get; `create --with-key` also mints the account's first API key |
| `domains` | domains + `traffic <domain> --range 1h\|6h\|24h\|7d\|30d`, `events <domain>` (per-event log), and the DMARC aggregate-report views: `dmarc <domain>` (enforcement readiness), `dmarc-sources <domain>`, `dmarc-reports <domain>` — all `--window 7d\|30d\|90d` |
| `routes` | address routes + `members list\|add\|remove\|replace` |
| `patterns` | per-domain pattern routes |
| `credentials` | a mailbox's IMAP/SMTP credentials (app-passwords) |
| `messages` | list/get/`raw`/`append`/`flag`/`label`/`move`/`delete`/`restore`/`trash empty`/`mime` |
| `labels` | list/create/rename/delete/`messages`/`expunge` |
| `threads` | list, get (with reply context) |
| `search <query>` | full-text search (`--group-thread` for one hit per conversation) |
| `sieve` | `scripts {list,get,put,delete,rename}`, `activate`/`deactivate`/`active`, `check`, `capabilities` |
| `pickups` | POP3 pickup sources: create/list/get/update/delete/`run` |
| `send` | submit an outbound message (compose or raw MIME) |
| `watch` | tail a mailbox's live events over WebSocket (`--until <glob>` exit on match, `--timeout <dur>`, `--exec <cmd>` per-event handler, `--fetch` hydrate message frames) |
| `deliver` | `check --to <addr>` (RCPT pre-flight), `inbound` (inject a test message) |
| `api` | call any route directly (escape hatch), `--list-routes` |
| `admin` | operator-only: `reindex`, `verify-login`, `pickup ingest\|report` (system keys) |
| `completion` / `upgrade` / `version` | shells, upgrade help, version |

Run `openemail <group> --help` for the full flag set of any command.

## Recipes

```sh
# Send mail — compose, or pipe a raw MIME message.
openemail send --from me@example.com --to you@example.com --subject Hi --text 'Hello!'
cat message.eml | openemail send --from me@example.com --to you@example.com
openemail send --from me@example.com --to you@example.com --file msg.eml --save

# Move a message between labels, mark it read, then read its raw body.
openemail messages move <id> --from INBOX --to Archive
openemail messages flag <id> --set seen
openemail messages raw <id> -o message.eml

# Import mail with IMAP APPEND (preserving INTERNALDATE).
cat old.eml | openemail messages append --label Archive --internaldate 1600000000

# Tail live events (JSON lines) while another process delivers mail.
openemail watch -m alice@example.com

# Block a script until the next message arrives (exit 0), then read its id.
id=$(openemail watch -m alice@example.com --until 'message.new' --timeout 30s | tail -n1 | jq -r .data.id)

# Run a handler per event (frame JSON on stdin) and hydrate message frames.
openemail watch -m alice@example.com --fetch --exec 'jq -r .message.subject'

# "Can I move this domain to p=reject yet?" — readiness, blockers, top sources.
openemail domains dmarc example.com --window 30d
openemail domains dmarc-sources example.com   # every sender, unaligned ones first
openemail domains dmarc-reports example.com   # the raw ingest log (empty ⇒ check rua=)

# Manage Sieve filters.
openemail sieve check -f filter.sieve            # dry-run compile (exit 1 if invalid)
openemail sieve scripts put main -f filter.sieve
openemail sieve activate main

# Rotate a webhook route's URL while KEEPING its signing secret (omit the secret).
openemail routes update hook@example.com --type webhook --webhook-url https://new/hook

# Hit any endpoint the CLI doesn't wrap yet.
openemail api GET /domains/example.com/traffic --query range=7d
openemail api POST /routes -d '{"address":"a@x.com","destinationType":"group"}'
openemail api --list-routes
```

## Configuration

- Config: `~/.config/openemail/config.toml` (honors `XDG_CONFIG_HOME`; same path
  on macOS). Secrets never live here — only a pointer to where the key is stored.
- Secrets: the OS keychain by default; a `0600` file under
  `~/.config/openemail/credentials/` as a **loud** fallback (a warning is
  printed). Force the file backend with `--no-keyring` / `OPENEMAIL_NO_KEYRING=1`.
- Profiles: `--profile <name>` (or `OPENEMAIL_PROFILE`) selects a named login;
  each has its own API URL, account, role, key, and default mailbox.

Precedence for every setting is **flag > environment > profile > default**.

| Setting      | Flag           | Environment              | Default                  |
|--------------|----------------|--------------------------|--------------------------|
| API URL      | `--api-url`    | `OPENEMAIL_API_URL`      | `https://api.open.email` |
| API key      | `--api-key`    | `OPENEMAIL_API_KEY`      | stored profile key       |
| Profile      | `--profile`    | `OPENEMAIL_PROFILE`      | `default`                |
| No keychain  | `--no-keyring` | `OPENEMAIL_NO_KEYRING`   | keychain preferred       |

`OPENEMAIL_NO_UPDATE_NOTIFIER=1` silences the passive "new version available"
check (which runs at most once per day, on a TTY only).

## Output & exit codes

- `stdout` carries data (the pipeable thing); `stderr` carries messages, prompts,
  warnings, and progress. They never intermix.
- `--json` emits machine-readable JSON. List outputs always include `nextCursor`;
  `--all` drains every page. Delivery/append results are echoed as core's exact
  response (so `duplicate:false` and null coordinates are preserved).
- One-time secrets (a new key/app-password token) print once to **stdout**, with a
  "shown once" warning on stderr.
- Exit codes: `0` ok · `1` error · `2` usage · `4` authentication required.
- Color is emitted only on a TTY with `NO_COLOR` unset and `--no-color` off.

See [docs/OUTPUT.md](docs/OUTPUT.md) for the `--json` contract and
[docs/COOKBOOK.md](docs/COOKBOOK.md) for worked flows.

## Principals

The CLI works with any core bearer:

- **account** key (`oek_…`, role account) — the everyday mode; manages its own
  tenant. `login` self-mints a per-device key from a pasted account key.
- **system** key (`oek_…`, role system) — operator mode; unlocks the `admin`
  group. Stored as-is (no minting).
- **mailbox** app password (`oemp_…`) — single-mailbox, limited scope. Directory
  and admin commands are unavailable; message/label/sieve/pickup/watch/send for
  its own mailbox work.

Cross-tenant lookups return `404` (not `403`) by design — "not found" means *"does
not exist, or is not accessible with this key"*.

## No telemetry

openemail sends **no** usage data, analytics, or crash reports. The only outbound
call it makes on its own is the once-a-day version check against the GitHub
releases API, which is TTY-only and disabled by `OPENEMAIL_NO_UPDATE_NOTIFIER`.

## Development

```sh
make build          # build ./bin/openemail
make test           # fast unit tests (httptest fakecore, no network)
make lint           # gofmt + go vet
make integration    # drives the binary against a local `wrangler dev` core
make live           # drives the binary against a DEPLOYED core (OE_HOST + OE_SYSTEM_KEY)
make snapshot       # dry-run a full goreleaser build matrix
```

Unit tests use an in-memory httptest fake of the core contract (no backend). The
integration target expects `wrangler dev` running on `:8787`, migrated and seeded
(`npm run db:migrate:local && npm run db:seed:local` in the core repo).

The **live** target (behind the `live` build tag) builds the binary and drives it
end-to-end over HTTPS against a **deployed** core, provisioning throwaway
tenants/domains/mailboxes and tearing them down — the CLI analogue of core's
`test/live` suite. It skips itself when `OE_HOST` / `OE_SYSTEM_KEY` are unset. See
[test/live/README.md](test/live/README.md).
