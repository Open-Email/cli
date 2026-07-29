# Cookbook

Worked flows. Set a default mailbox first so `-m` is implicit:

```sh
openemail login
openemail mailboxes use alice@example.com
```

## Provision a new mailbox that receives mail

```sh
# 1. The domain. It only exists once you PROVE you control it: publish the TXT
#    record `openemail whoami` shows, then run create. Re-running create is the
#    onboarding loop — it re-checks DNS and activates sending once SPF and both
#    DKIM CNAMEs resolve. (Receiving needs nothing but your MX pointing at us.)
openemail whoami                        # shows _openemail.<domain> TXT to publish
openemail domains create example.com    # refuses with the record until it is live
openemail domains dns example.com       # what is still missing, and what resolves

# 2. The mailbox — passing --address CLAIMS it: the address→mailbox route and
#    the label are created atomically, so the mailbox receives mail from this
#    one call. A taken address answers `address_taken` and creates nothing.
openemail mailboxes create --address alice@example.com

# 3. (Optional) An IMAP/SMTP login. The username defaults to the primary
#    address, which already routes to the mailbox after step 2.
openemail credentials create <mailboxId> --kind app-password

# Verify the chain end-to-end — 200 means mail to alice@ is deliverable.
openemail deliver check --to alice@example.com
```

`--address` is the whole provisioning step; there's no separate route to create.
Give a mailbox **additional** addresses (aliases, `+tags`, a second domain) with
the routes API — and it's idempotent, so re-running a bind is a safe no-op:

```sh
openemail routes create sales@example.com --type mailbox --mailbox <mailboxId>
openemail mailboxes update <mailboxId> --address sales@example.com   # relabel to a routed address
```

Omit `--address` on create for a bare store (POP3 pickup target, APPEND-only
archive) that receives nothing until you bind a route.

## Send a message

```sh
# Compose a simple text message.
openemail send --from alice@example.com --to bob@example.com \
  --subject "Lunch?" --text "Are you free Thursday?"

# Send a fully-formed MIME message (raw mode) and keep a Sent copy.
openemail send --from alice@example.com --to bob@example.com --file message.eml --save
cat message.eml | openemail send --from alice@example.com --to bob@example.com
```

The result is one of: `delivered` (stored locally), `filtered` (the recipient's
Sieve discarded it — accepted, nothing stored), or `queued` (webhook / remote
forward / group / relay — the final outcome shows up in the traffic log). Each
send generates a ULID `X-Delivery-Id` and reuses it on retry, so a 5xx never
duplicates the message.

## Read a message and download an attachment

```sh
# Metadata only (flags, labels, envelope):
openemail message get 01MSGID…

# The email-client view: decoded headers, the text/html body, and an
# attachment list (each with a `section` handle).
openemail message content 01MSGID…

# Pull one part by its section number (from the attachments list). Decoded by
# default; -o dir/ saves it under the server-suggested filename.
openemail message part 01MSGID… 2 -o ./downloads/
openemail message part 01MSGID… 2 --raw -o invoice.b64   # the stored encoded slice

# The whole raw RFC822 message:
openemail message raw 01MSGID… -o message.eml
```

`content` and `part` serve **live** messages (a trashed message is `404`, like
`raw`); over-large messages answer `413` with `raw` as the fallback. Add `--json`
to `content` for the structured object.

## Work in the console

```sh
# Full-screen console: sidebar of resources, tables, detail views, and forms.
# `n` creates, `e` edits, `d` deletes (always behind a y/esc confirm), `/`
# filters, `esc` backs out, `q` quits. `M` on a group route edits its members;
# `D` on the Mailboxes screen shows restorable tombstones, where `u` restores.
# Open a mailbox for a LIVE message list (fed by the events WebSocket): enter
# previews, `c` composes a message FROM that mailbox (same semantics as
# `openemail send` compose mode, with a Save-to-Sent toggle), `t` toggles read,
# `!` toggles flagged, `l` edits labels (a move is one atomic patch), `d` moves
# to trash, `T` opens the trash view where `u` restores.
openemail ui
```

Console mutations mirror the flag commands' semantics exactly — create-and-bind
for mailbox addresses, atomic destination replace for routes/patterns, webhook
secrets preserved when left empty on edit. The rare destructive operations
(domain delete, purges, trash empty) stay CLI-only on purpose. Needs an account
or system key (a mailbox app-password can't browse the directory).

## Tail live events

```sh
# JSON lines to stdout; a reminder + status on stderr. Ctrl-C to stop.
openemail watch

# One-shot (no auto-reconnect):
openemail watch --reconnect=false
```

Events are **signals only** (identifiers, never content). On each frame, re-sync
via the REST commands (`messages get`, `threads get`, …). The session auto-renews
across the 12h server TTL.

## Migrate mail in with IMAP APPEND

```sh
# Import a maildir, preserving each message's original date.
for f in ~/Maildir/cur/*; do
  ts=$(date -r "$f" +%s)
  openemail messages append --label Archive --internaldate "$ts" -f "$f"
done
```

APPEND bypasses the routing ladder and never dedups — two identical appends are
two messages. Pass `--filter` to run the mailbox's active Sieve script on the
imported message.

## Manage Sieve filters

```sh
openemail sieve capabilities                       # supported extensions + limits
openemail sieve check -f filter.sieve              # dry-run compile (exit 1 if invalid)
openemail sieve scripts put main -f filter.sieve   # 422 shows the line:col on error
openemail sieve activate main
openemail sieve active                             # which script is active
openemail sieve scripts get main -o backup.sieve
```

## Rotate a webhook route's URL (keep the secret)

```sh
# Omitting --webhook-secret preserves the existing signing secret (write-only).
openemail routes update hook@example.com --type webhook \
  --webhook-url https://new.example.com/hook

# Change or clear the secret explicitly:
openemail routes update hook@example.com --type webhook \
  --webhook-url https://new.example.com/hook --webhook-secret 's3cr3t'
openemail routes update hook@example.com --type webhook \
  --webhook-url https://new.example.com/hook --clear-webhook-secret
```

## Configure a POP3 pickup source

```sh
openemail pickups create --host pop.gmail.com --port 995 --username you@gmail.com \
  --tls tls --interval 15 --name "gmail import"     # password prompted (or --password)
openemail pickups list
openemail pickups run <id>                          # fetch now
openemail pickups update <id> --disabled            # pause it
```

The password is write-only — never returned, and omitted on update to keep the
current one.

## Empty the trash / purge

```sh
openemail messages list --trash                     # what's in the trash
openemail messages restore <id>                     # undo one soft-delete
openemail messages trash empty                       # purge all (typed confirm)
openemail messages delete <id> --purge               # hard-delete one (typed confirm)
```

## Scripting with a system key (operator)

```sh
export OPENEMAIL_API_KEY=oek_system_…
openemail admin verify-login alice@example.com --password …   # check IMAP/SMTP creds
openemail admin reindex <mailboxId>                            # rebuild the FTS index
openemail --json api GET /accounts | jq '.accounts[].id'      # any route + jq
```
