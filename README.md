# openemail

The command-line client for the [OpenEmail](https://open.email) platform — in the
style of the Stripe CLI and wrangler. Authenticate once in your browser; the CLI
gets a dedicated per-device API key, stores it in your OS keychain, and uses it
for every command. It covers the **entire** core API: the directory plane (accounts,
domains, routes, patterns, credentials, identities), the mailbox data plane
(messages, labels, threads, search, rules, sieve, pickups), calendars and contacts
(`calendars`, `addressbooks`, `pim` — raw iCalendar/vCard objects, sharing,
RSVP, feeds), and the workflow verbs (`send`, `watch`, `deliver`, `api`,
operator `admin`).

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
# Log in — opens a browser, you approve in the console, and the key it mints
# lands in your OS keychain. Nothing is pasted anywhere.
openemail login

# No browser (SSH, a container)? A short code you can approve from any machine.
openemail login --device

# Non-interactive / CI:
OPENEMAIL_API_KEY=oek_… openemail login --api-key oek_…

# Or paste a key you already hold:
openemail login --paste

openemail whoami         # who this key resolves to (+ your domain-verification TXT)
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
| `ui` (alias `console`) | full-screen console: sidebar + tables for mailboxes/domains/routes/patterns/keys/accounts (plus Suppressions and DKIM on a system key) with create/edit forms, confirm-gated deletes, and group-member editing. A mailbox row drills into credentials, filters (and Sieve scripts behind them), calendars, addressbooks, pickups and preferences; enter opens a live message list (events WebSocket) with compose, reply, search, previews (`S` shows the raw MIME source), invitation RSVP, junk training, flag toggles, label editing, and a trash view with restore |
| `mailboxes` | create/list/get/update/delete/restore, `use` (set default), `webhook {get,set,delete,test}` (the per-mailbox event webhook: signed, fact-only event batches; the secret is write-only — `--secret` rotates, `--clear-secret` removes, omit keeps) |
| `keys` | account API keys: create/list/revoke |
| `accounts` | accounts (create/list/update are system-only), get, `traffic`, `send-usage`; `create --with-key` also mints the account's first API key; `update --freeze` is the tenant-scale send STOP and `update --pause` its reversible HOLD (both cover every mailbox on every domain the account owns, queued relay backlog included, and neither touches inbound — but a freeze bounces that backlog while a pause defers it), `update --send-*-per-day` sets the tenant-scale volume caps, `traffic` is the cross-mailbox rollup of what went OUT and `send-usage` of what is LEFT |
| `domains` | domains (`create` is create-or-advance: requires your verification TXT, activates sending once SPF + both DKIM CNAMEs resolve), `dns <domain>` (required records + liveness) + `traffic <domain> --range 1h\|6h\|24h\|7d\|30d`, `events <domain>` (per-event log), `webhook {get,set,delete,test} <domain>` (the per-domain event webhook — every mailbox on the domain plus lifecycle and send outcomes; take a baseline after `set`), and the DMARC aggregate-report views: `dmarc <domain>` (enforcement readiness) and `dmarc-sources <domain>` take `--range 7d\|30d\|90d`; `dmarc-reports <domain>` pages the raw reports over the full retention (`--limit/--cursor/--all`, no window) |
| `routes` | address routes + `members list\|add\|remove\|replace` |
| `patterns` | per-domain pattern routes |
| `credentials` | a mailbox's login credentials (app-passwords; `--expires-in` for session-scoped ones, @-free usernames for mail-less identities) |
| `identities` | `get [id]` — the durable identity and its bound stores (mail + PIM facets with usage); a calendar-only identity shows no mail facet |
| `messages` | list/get/`raw`/`append`/`compose` (file from fields without sending — drafts, imports)/`junk`/`not-junk` (train this mailbox's spam filter)/`flag`/`label`/`move`/`delete`/`restore` (many ids restore in one atomic call)/`trash empty`/`mime` |
| `labels` | list/create/rename/delete/`messages`/`expunge` |
| `threads` | list, get (with reply context), `reply` — the server derives recipient, subject and threading headers from the conversation |
| `search [query]` | full-text search, or a structured filter search when any of `--from/--to/--before/--after/--unread/--has-attachment/--sort/…` is passed (`--snippet` for highlighted excerpts, `--total` for the match count) |
| `rules` | filter rules — the simple alternative to writing Sieve: `list`/`get`/`put`/`delete`, plus `add`/`remove`/`enable`/`disable`/`move` edits and `script` (the Sieve they compile to) |
| `sieve` | `scripts {list,get,put,delete,rename}`, `activate`/`deactivate`/`active`, `check`, `capabilities` |
| `vacation` (alias `ooo`) | out-of-office auto-reply: `show`/`set`/`on`/`off` — one reply per correspondent per absence, compare-and-swap on the document's state |
| `compose` | send a structured message: multiple recipients, `--attach` files (staged as uploads), one result per recipient |
| `calendars` | calendars: list/create/get/update/delete, `objects` (alias `events`: list with `--start/--end/--expand` range queries, get/put/delete/move raw .ics or `--json` JSCalendar), `respond` (RSVP), `invitations {show,status,respond}` (read and answer an invitation straight from the email it arrived in), `changes` (sync diff), `export`/`import`, `shares`, `tokens` (feed URLs) |
| `addressbooks` | addressbooks: the same verb set over raw .vcf objects (alias `contacts`; no range/RSVP) |
| `pim` | cross-collection surfaces: `shared` (shared with me), `public` (directory), `subscribe`/`unsubscribe`/`subscriptions`, `feed <token-or-url>` (no login needed) |
| `prefs` | the opaque client-preferences document: `get`/`put`/`set` with compare-and-swap on its version |
| `pickups` | POP3 pickup sources: create/list/get/update/delete/`run` |
| `send` | submit an outbound message (compose or raw MIME) |
| `do-not-send` (alias `suppressions`) | the account's OWN do-not-send list: `list`/`check`/`add`/`remove` — addresses this account never mails; `remove` also lifts a platform hard-bounce block on the address (the deployment-global evidence list stays under `admin suppressions`). One address list, kept as its own command because it is the combination people want most |
| `lists` | address lists in full: named allow/block lists at account, domain or mailbox scope, inbound or outbound. `list`/`show`/`create`/`rename`/`delete`, `add`/`remove`/`import` patterns, and `check` — the same evaluator the delivery path runs, naming the pattern that decided |
| `watch` | tail a mailbox's live events over WebSocket (`--until <glob>` exit on match, `--timeout <dur>`, `--exec <cmd>` per-event handler, `--fetch` hydrate message frames) |
| `deliver` | `check --to <addr>` (RCPT pre-flight), `inbound` (inject a test message) |
| `api` | call any route directly (escape hatch) |
| `admin` | operator-only (system keys): `reindex`, `verify-login`, `pickup ingest\|report`, `suppressions {list,get,add,lift}` (the deployment-global do-not-send list), `dkim {status,rotate,activate}` (platform signing keys) |
| `completion` / `upgrade` / `version` | shells, upgrade help, version |

Run `openemail <group> --help` for the full flag set of any command.

## Recipes

```sh
# Send mail — compose, or pipe a raw MIME message.
openemail send --from me@example.com --to you@example.com --subject Hi --text 'Hello!'
cat message.eml | openemail send --from me@example.com --to you@example.com
openemail send --from me@example.com --to you@example.com --file msg.eml --save

# List by the Date header rather than by arrival (the two disagree for delayed,
# clock-skewed and imported mail; the DATE column follows whichever you pick).
openemail messages list --sort-by date
openemail threads list --sort-by date --order asc

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
openemail domains dmarc example.com --range 30d
openemail domains dmarc-sources example.com   # every sender, unaligned ones first
openemail domains dmarc-reports example.com   # the raw ingest log (empty ⇒ check rua=)

# Filter mail without writing Sieve: rules are evaluated top to bottom.
openemail rules add --name "Acme" --if 'from:contains:@acme.com' --then 'label:Work'
openemail rules add --name "Big" --if 'size:over:5m' --then 'label:Big' --stop
openemail rules list                             # order, state, and whether they're active
openemail rules move 2 1                         # reorder (order is meaning)
openemail rules disable 1                        # keep the rule, stop it running
openemail rules script                           # the Sieve it compiles to

# Search: text, or structured filters over any field.
openemail search invoice                                    # full text
openemail search --from boss@acme.com --after 7d --total    # structured
openemail search invoice --snippet --sort receivedAt:desc   # highlighted excerpts
openemail search --unread --has-attachment --label INBOX --position 25

# Send with attachments to several recipients (server assembles the MIME).
openemail compose --from me@example.com --to a@x.com --to b@x.com \
  --cc c@x.com --subject "Report" --text "See attached." --attach report.pdf

# A multi-recipient send can partly fail: one result per recipient, exit 1.
# Retry with the SAME --delivery-id and only the failures are re-attempted —
# without it, everyone who already got the message gets it twice.
openemail compose --delivery-id 01J8ZQ... --from me@example.com \
  --to a@x.com --to b@x.com --subject "Report" --text "See attached."

# Reply to a conversation. Prefer this over `compose`: the recipient, the
# "Re: …" subject and the In-Reply-To/References chain are derived server-side,
# which is where every mail client's threading quietly breaks.
openemail threads reply <threadId> --text "On it, thanks."
openemail threads reply <threadId> --body-file reply.txt --attach notes.pdf --show-context

# File a message without sending it — a draft, an import, a seeded thread.
openemail messages compose --from me@example.com --to you@example.com \
  --subject "Half written" --text "…" --draft        # flags draft,seen into Drafts

# Teach this mailbox's spam filter. Training only: nothing is moved or flagged.
openemail messages junk <id>
openemail messages not-junk <id>

# Manage Sieve filters.
openemail sieve check -f filter.sieve            # dry-run compile (exit 1 if invalid)
openemail sieve scripts put main -f filter.sieve
openemail sieve activate main

# Calendars: create one, add an event, list a month expanded into occurrences.
openemail calendars create work --display-name "Work"
openemail calendars objects put work standup.ics --file standup.ics
openemail calendars events list work --start 2026-08-01 --end 2026-09-01 --expand

# RSVP to an invitation that landed in your calendar.
openemail calendars respond work invite.ics accepted

# …or answer one straight from the email it arrived in (files it if it's new).
openemail calendars invitations show <messageId>       # event, and can you answer it?
openemail calendars invitations respond accepted --message <messageId>

# Out of office. Turning it ON starts a new absence (everyone becomes eligible
# for one reply again); `set` edits the wording without re-notifying anyone.
openemail vacation on --subject "Away until the 15th" --text "Back on the 15th."
openemail vacation set --text "Back on the 20th, not the 15th."
openemail vacation off

# Read or edit an event as JSCalendar instead of raw .ics.
openemail calendars objects get work standup.ics --json
openemail calendars objects put work standup.ics --json --file event.json

# Share a calendar with a teammate, or publish a read-only feed URL.
openemail calendars update work --visibility shared
openemail calendars shares set work teammate@example.com --permission read-write
openemail calendars tokens create work --label "team feed" --expires-in 90d

# Import contacts and read one back.
openemail addressbooks import default --file contacts.vcf
openemail addressbooks objects get default --uid alice-uid -o alice.vcf

# A calendar-only identity: a store with no address, plus an @-free login.
openemail mailboxes create                        # no --address
openemail credentials create <id> --kind password --username roomcal --password …
openemail identities get <id>                     # facets: pim bound, no mail store

# Rotate a webhook route's URL while KEEPING its signing secret (omit the secret).
openemail routes update hook@example.com --type webhook --webhook-url https://new/hook

# Tenant: never mail this address again (account-scoped; someone unsubscribed).
openemail do-not-send add them@example.net --note "asked to stop, 2026-08-14"
openemail do-not-send list --all
openemail do-not-send remove them@example.net   # also lifts a platform bounce block

# Tenant: refuse INCOMING mail from a domain, for the whole account.
openemail lists create "Blocked senders" --direction inbound --verdict block
openemail lists add <list-id> @spammer.example
# …and rescue one sender from that block, for one mailbox only. A narrower
# allow also exempts them from the spam filter.
openemail lists create "Trusted" --direction inbound --verdict allow --scope mailbox:<id>
openemail lists add <list-id> partner@spammer.example
openemail lists check partner@spammer.example --direction inbound --scope-mailbox <id>

# Operator: is an address on the do-not-send list, and why? (system key)
openemail admin suppressions get bounced@example.com
openemail admin suppressions list --all
openemail admin suppressions lift bounced@example.com   # only once the cause is fixed

# Operator: a real complaint the feedback-loop consumer refused to act on.
# It suppresses only what it can prove we sent, so a complaint about mail with
# no correlatable submission is logged (`fbl_suppression_refused`) and dropped —
# leaving the platform still mailing a complainant. This closes that by hand.
openemail admin suppressions add angry@example.com --note "AOL FBL, ticket 4711"

# Operator: what is signing outbound mail, and what must a customer publish?
openemail admin dkim status --domain example.com   # paste-ready CNAME rows
openemail admin dkim rotate                        # stage the next key (7-day soak)
openemail admin dkim activate                      # flip early, once DNS resolves

# Hit any endpoint the CLI doesn't wrap yet.
openemail api GET /domains/example.com/traffic --query range=7d
openemail api POST /routes -d '{"address":"a@x.com","destinationType":"group"}'
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
