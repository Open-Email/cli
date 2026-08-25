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
#    onboarding loop — it re-checks DNS and activates sending once the
#    oe-bounce CNAME and both DKIM CNAMEs resolve. (Receiving needs nothing
#    but your MX pointing at us.)
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

## Reply to a conversation

```sh
# Everything but the body is derived from the thread.
openemail threads reply 01THREADID… --text "On it, thanks."

# See who is about to be answered before it goes.
openemail threads reply 01THREADID… --body-file reply.txt --show-context

# With an attachment, and without keeping a Sent copy.
openemail threads reply 01THREADID… --text "Signed." --attach contract.pdf --no-save
```

Prefer this over `compose` whenever a reply is what you mean. There is no `--to`,
no `--subject` and no `--in-reply-to` on purpose: the answerable address (Reply-To
before From, and the *recipient* when the newest member is your own Sent copy),
the `Re: …` subject, and a trimmed `References` chain are all computed server-side.
Rebuilding those client-side is the classic mail-client bug — a broken chain is
invisible to the sender and only shows up as someone else's client failing to
group the conversation.

`--subject` overrides the derived subject, which **renames the thread** for the
recipient; pass it only when you mean that. A thread with nobody to answer (a
null-sender bounce, say) is refused with `no_reply_target` rather than sent into
the void. The result is the same per-recipient shape as `compose`.

## File a message without sending it

```sh
# A draft: flagged draft+seen and filed into Drafts.
openemail messages compose --from alice@example.com --to bob@example.com \
  --subject "Half written" --text "…" --draft

# An import into an arbitrary label, backdated.
openemail messages compose --from old@example.com --to alice@example.com \
  --subject "From the archive" --body-file old.txt \
  --label Archive --internaldate 1600000000
```

Routing, relay and the Sent copy are all bypassed — nothing leaves the platform.
Use `openemail compose` when the message is meant to go somewhere, and
`openemail messages append` when you already hold raw RFC 5322 bytes rather than
fields. An explicit `--label` wins over `--draft`'s default.

## Train the spam filter

```sh
openemail messages junk 01MSGID…       # this is spam
openemail messages not-junk 01MSGID…   # this was wrongly classified
```

The sample trains **this mailbox's** personal overlay, so one person's idea of
junk never becomes another's. It is training only: nothing is moved, flagged or
deleted — pair it with `openemail messages move` if you also want it out of the
inbox. Accepted fire-and-forget (a success means submitted, not learned), and
repeated calls on the same message dedupe filter-side. A deployment with no spam
filter configured answers `learning_unavailable`.

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
# `openemail send` compose mode, with a Save-to-Sent toggle), `R` replies to the
# selected message's thread, `/` opens a search form, `t` toggles read,
# `!` toggles flagged, `l` edits labels (a move is one atomic patch), `s`/`S`
# report junk / not-junk, `d` moves to trash, `T` opens the trash view where
# `u` restores. In a preview, `i` opens a calendar invitation to RSVP.
openemail ui
```

A mailbox row on the Mailboxes screen drills into its sub-resources: `C`
credentials, `F` filter rules (and `s` from there for the Sieve scripts behind
them), `c` calendars, `b` addressbooks, `p` POP3 pickups, `P` preferences, `V`
the out-of-office auto-reply. With a
system key the sidebar also carries **Suppressions** (the do-not-send list, `u`
lifts an entry) and **DKIM** (key generations, `n` stages a rotation, `A`
activates a staged key).

Two screens deliberately state a verdict rather than just listing rows, because
the row set alone does not answer the question people open them for: Filters and
Sieve both say whether anything is *actually* filtering delivered mail (only one
script can be active across both surfaces), and Pickups flags sources core has
auto-disabled — a broken pickup is otherwise silent, mail just stops arriving.

Console mutations mirror the flag commands' semantics exactly — create-and-bind
for mailbox addresses, atomic destination replace for routes/patterns, webhook
secrets preserved when left empty on edit, compare-and-swap on a preferences
edit. The rare destructive operations (domain delete, purges, trash empty,
forcing a DKIM activation past the resolver check) stay CLI-only on purpose.
Needs an account or system key (a mailbox app-password can't browse the
directory).

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

## Filter incoming mail without writing Sieve

```sh
openemail rules add --name "Acme" --if 'from:contains:@acme.com' --then 'label:Work'
openemail rules add --name "Newsletters" --if 'listId:exists' --then 'label:Lists' --then 'flag:seen' --stop
openemail rules list          # order, on/off, and whether the rules are the ACTIVE filter
openemail rules script        # read the Sieve they compile to
```

Conditions are `[!]field:op[:value]` (`from to cc toOrCc subject listId header body
size`; ops `contains is matches regex exists`, `body:contains`, `size:over|under`);
the `!` prefix inverts one. Actions are `label:<name>`, `flag:seen|answered|flagged`,
`redirect[-copy]:<addr>`, or `discard`.

Rules and hand-written Sieve are **two interfaces over one active filter**: saving
rules deactivates an active hand-written script, and `openemail sieve activate <name>`
deactivates the rules. `rules list` always says which is actually filtering mail.
In the console, `F` on a mailbox opens the same list with `t` toggle, `J`/`K` reorder,
and `d` delete.

## Find a message

```sh
openemail search invoice                                     # full text
openemail search --from boss@acme.com --after 7d --total     # structured filters
openemail search invoice --snippet                           # highlighted excerpts
openemail search --unread --has-attachment --sort receivedAt:desc --limit 10
openemail search --from a@x.com --position 25                # next page (offset paging)
```

Any structured flag switches from the text search to the filter search, which pages
by `--position` instead of a cursor. Core allows at most one full-text condition, so
a bare query and `--subject`/`--body` are mutually exclusive. Dates take RFC3339,
`YYYY-MM-DD`, or a relative `7d`/`24h`; sizes take a `k`/`m`/`g` suffix.

## List by the date the sender wrote, not by arrival

```sh
openemail messages list --sort-by date              # the Date header
openemail messages list --sort-by date --order asc  # oldest first
openemail threads list --sort-by date               # conversations, same key
openemail messages list --trash --sort-by date      # the trash tier too
```

A message carries two dates and they do not have to agree: `receivedAt` (when it
reached the mailbox) and the `Date:` header (when the sender says they wrote it).
They diverge for a delayed relay, a sender with a wrong clock, a POP3 backfill,
and imported mail — and that is when a listing looks out of order.

`--sort-by arrival` is the default and lists in the order mail landed here.
`--sort-by date` is what a mail client's Date column implies, and is how to make
a listing agree with what Thunderbird or the webmail shows. A message with no
parseable `Date:` header sorts by its arrival rather than sinking to the end.
The **DATE column follows the flag**, so the dates you see always explain the
order you see.

Cursors follow the key, so hold `--sort-by` constant while paging: a cursor from
the other key is refused (`invalid_cursor`) rather than quietly returning a
differently-windowed page. `--all` holds it for you.

## Send with attachments

```sh
openemail compose --from me@example.com --to you@example.com \
  --subject "Q3 report" --text "See attached." --attach report.pdf --attach data.csv
```

`compose` sends a structured message: each `--attach` is staged with an upload first
(bytes never ride inside the JSON), the server assembles the MIME, and you get one
result per recipient — so a partial failure names the address that failed and exits
non-zero. `--to/--cc/--bcc` repeat and also accept `"Name <addr>"` and comma lists.
Use `openemail send --file` instead when you already hold raw RFC 5322 bytes.

## Keep a team calendar

```sh
openemail calendars create work --display-name "Work" --color '#0A84FF'
openemail calendars objects put work standup.ics --file standup.ics
openemail calendars events list work --start 2026-08-01 --end 2026-09-01 --expand

# Share it read-write with a teammate (grant + visibility are BOTH required),
# and hand read-only access to everyone else as an unauthenticated feed URL.
openemail calendars update work --visibility shared
openemail calendars shares set work teammate@example.com --permission read-write
openemail calendars tokens create work --label "team feed" --expires-in 90d

# The teammate reads it by ULID (find it via `openemail pim shared`):
openemail pim shared
openemail calendars objects list <collectionId> --owner <ownerMailboxId>
```

An organizer's `objects put`/`delete` fans out iTIP invitations (REQUEST/CANCEL)
to the attendees the event names; `calendars respond <cal> <href> accepted`
records your RSVP and tells the organizer — a local organizer's copy is patched
in place, a remote one is mailed a `METHOD:REPLY`.

## Track to-dos

```sh
openemail calendars tasks add tasks "Buy milk" --due 2026-08-10
openemail calendars tasks list tasks --open
openemail calendars tasks done tasks <href>
openemail calendars tasks set tasks <href> --due 2026-08-14 --priority 1
```

A to-do is a `VTODO` stored in a calendar — same collection, same href, same
ETag, same sharing and feed tokens as an event — so there is no separate `tasks`
tree, and `calendars objects list <cal> --component task` reaches the same rows.
What `tasks` adds is honesty about the differences: core keeps a to-do's **DUE**
in the field an event uses for its end, most to-dos have no start at all, and
`STATUS` is the only thing that says whether one is finished. So the task table
shows `DUE` and `STATUS`, and the mixed listing labels the shared column
`END/DUE`.

`done` writes three properties, not one — `STATUS:COMPLETED`,
`PERCENT-COMPLETE:100` and the `COMPLETED` timestamp — which is why setting
`STATUS` by hand leaves other clients showing the task as partly done.
`--reopen` is the exact inverse. `set` is a read-modify-write of the JSCalendar
Task under `If-Match`, so recurrence, alarms and every property core preserved
through its escape hatches survive an edit untouched.

A bare `YYYY-MM-DD` due is a whole-day deadline; pass an RFC3339 timestamp for a
particular moment. `--open` filters client-side, so pair it with `--all` on a
calendar bigger than one page.

Core schedules to-dos as well as events (RFC 5546 §3.4), so an assigned task
arrives as mail and is answered exactly like an invitation — see below.
Completing a task somebody assigned you updates your own copy only: the iTIP
fan-out belongs to the organizer, so tell them with `calendars respond`. In the
console (`openemail ui`), `t` on a calendar row toggles a to-do done or reopens
it.

## Answer an invitation that arrived by email

```sh
openemail calendars invitations show <messageId>     # the event, and can you answer it?
openemail calendars invitations respond accepted --message <messageId>
```

Name the message and the server does the rest: it locates the scheduling part,
parses it, files an attendee copy into your default calendar if the event is not
stored yet, records the reply, and tells the organizer — patching their copy when
they are local, mailing a `METHOD:REPLY` when they are not. Re-answering later is
the same command; it updates the existing copy instead of filing a second one.

`show` is worth running first because it answers a question you cannot work out
locally: **can answer** is the respond endpoint's own verdict. You cannot answer
your own invitation (an organizer edits their copy instead), nor a message that is
itself someone else's reply or a cancellation — and being the organizer is decided
on any routing tier, so an invitation that reached you through an alias still
counts as yours.

`--section` pins a particular part when a message carries more than one; omitted,
the server picks the message's own scheduling part.

For an `.ics` that is not in a mailbox at all — a file someone handed you — the
inline form still applies, and reads stdin when `--file` is omitted:

```sh
openemail calendars invitations respond accepted --file invite.ics
```

Prefer the `--message` form otherwise. The bytes are already on the server, so
piping the part in only makes the client fetch them, decode the charset, and post
them straight back. For an event already in a calendar, `openemail calendars
respond <calendar> <href> <partstat>` addresses it directly.

## Set an out-of-office auto-reply

```sh
openemail vacation show
openemail vacation on --subject "Away until the 15th" --text "Back on the 15th." \
  --from 2026-08-01 --to 2026-08-15
openemail vacation set --text "Back on the 20th, not the 15th."   # fix the wording
openemail vacation off
```

Core sends one reply per correspondent **per absence**, so a mailing list or a
persistent sender is not answered over and over.

The distinction between `on` and `set` is the part worth knowing, because getting
it wrong is embarrassing rather than merely wrong: turning the reply **on** starts
a new absence, which makes everyone eligible for a reply again — including people
who already heard from you during the last one. Editing while you are already away
deliberately re-notifies nobody. So fix a typo with `set`; never with `off` then
`on`.

Dates are optional and bound the absence (`--from ''` clears one). They are given
as `YYYY-MM-DD`, an RFC3339 instant, or unix seconds, and are normalized to UTC.
Relative forms like `7d` are refused here — they mean "ago" elsewhere in the CLI,
which would silently backdate the start of an absence.

Writes are compare-and-swap on the document's state, so a change made from another
device since you read it is refused (`--force` overrides, `--if-match <state>`
pins an explicit one). An unmentioned field keeps its value; a flag given empty
clears it.

## Edit calendar and contact objects as JSON

```sh
openemail calendars objects get work standup.ics --json > event.json
# edit event.json …
openemail calendars objects put work standup.ics --json --file event.json
```

`--json` reads and writes JSCalendar (RFC 8984) / JSContact (RFC 9553) instead of
raw iCalendar/vCard, so a program never has to parse wire text. Core converts on the
way in, which means ETags, `--if-match`, and invitation fan-out behave identically.
A read reports `writable:false` when an object converts to JSON but could never be
converted back — the CLI warns rather than letting you edit something unsavable.

## Store client preferences

```sh
openemail prefs get                          # the document plus its version
openemail prefs set theme=dark density=3     # edit single keys (CAS-guarded)
openemail prefs set zip='"01234"'            # quotes force a string, not a number
openemail prefs get --raw > prefs.json       # back it up
openemail prefs put --file prefs.json        # restore (version from the file guards it)
```

The blob is opaque to core — clients own its schema — and belongs to the **identity**,
so it follows a user across mail, calendars, and devices. Every write is a
compare-and-swap on `version`: a stale write is refused (412) instead of silently
clobbering another device. `--force` overrides that deliberately.

## Migrate contacts in

```sh
openemail addressbooks import default --file contacts.vcf   # idempotent (hrefs derive from UIDs)
openemail addressbooks contacts list default
openemail addressbooks objects get default --uid <uid> -o one.vcf
```

## A calendar-only identity (no email at all)

```sh
openemail mailboxes create                                  # no --address: a bare store
openemail credentials create <id> --kind password --username roomcal --password …
openemail identities get <id>                               # pim facet bound, mail facet absent
```

The @-free username skips the address-ownership check — such a login works for
DAV/JMAP clients but can never send or receive mail. `openemail whoami` and
`openemail admin verify-login` both report the identity id and its facets.

## Empty the trash / purge

```sh
openemail messages list --trash                     # what's in the trash
openemail messages restore <id>                     # undo one soft-delete
openemail messages restore <id> <id> <id>           # undo a bulk delete, atomically
openemail messages trash empty                       # purge all (typed confirm)
openemail messages delete <id> --purge               # hard-delete one (typed confirm)
```

Several ids are **one call** against the mailbox, and that is the point: undoing a
bulk delete lands in a single commit rather than as an arbitrary partial subset if
something fails halfway. Up to 200 at a time. Each message comes back under the
labels it had before it was expunged (or INBOX if none survive), with fresh UIDs
as IMAP requires.

An id that is missing, already live, or already purged is reported `not_found` on
its own row without stopping the others, and the command exits non-zero so a
script notices without parsing the table.

## Receive events on your laptop

The event webhook POSTs signed, fact-only batches — ids, states, an actor and
a sequence, never content — to one URL per mailbox or per domain. To see them
locally, put a tunnel in front of a local receiver and point the hook at it:

```sh
# any tunnel that gives you a public HTTPS host works; e.g. cloudflared
cloudflared tunnel --url http://localhost:8787 &
openemail domains webhook set acme.example --url https://<tunnel-host>/events --secret "$(openssl rand -hex 32)"
openemail domains webhook test acme.example     # queues one webhook.test batch
openemail domains webhook get acme.example      # last delivered / last failure
```

The secret is stored write-only — keep your copy. `set` again with `--url`
alone keeps it; `--secret` rotates it (the next attempt signs with the new
one); `--clear-secret` removes it. A hook failing for 24h is auto-disabled
and any `set` re-enables it.

## Verify a batch

Two steps, in this order, and the order is the whole point: verify the HMAC
over the RAW body bytes first, THEN parse and check `sentAt` against your
tolerance. The mistake every framework invites is hashing a re-serialised
object — key order, whitespace and number formatting all change the bytes.
Read the raw buffer.

```js
// Node (express with a raw body)
import { createHmac, timingSafeEqual } from "node:crypto";
app.post("/events", express.raw({ type: "application/json" }), (req, res) => {
  const expected = "sha256=" + createHmac("sha256", SECRET).update(req.body).digest("hex");
  const got = req.get("X-OpenEmail-Signature") ?? "";
  if (got.length !== expected.length || !timingSafeEqual(Buffer.from(got), Buffer.from(expected))) return res.sendStatus(401);
  const batch = JSON.parse(req.body);
  if (Math.abs(Date.now() - batch.sentAt) > 5 * 60_000) return res.sendStatus(401);
  // dedupe on batch.id, order by batch.sequence, reconcile on events.gap
  res.sendStatus(200);
});
```

```go
// Go
raw, _ := io.ReadAll(r.Body)
mac := hmac.New(sha256.New, secret); mac.Write(raw)
want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
if !hmac.Equal([]byte(r.Header.Get("X-OpenEmail-Signature")), []byte(want)) { w.WriteHeader(401); return }
var batch struct{ ID string; SentAt int64 `json:"sentAt"` }
_ = json.Unmarshal(raw, &batch)
```

```python
# Python
raw = request.get_data()                       # bytes, before any parse
want = "sha256=" + hmac.new(SECRET, raw, hashlib.sha256).hexdigest()
if not hmac.compare_digest(request.headers.get("X-OpenEmail-Signature", ""), want): abort(401)
batch = json.loads(raw)
```

Answer 2xx as soon as the batch is durably yours — a 30s handler is a failed
attempt, and every non-2xx (3xx included) retries for ~3 days.

## Start from a baseline, resync after a gap

A domain `set` arms every mailbox on the domain in the background — the
response is not a promise about mutations already in flight. Take a baseline
right after it returns, and reconcile from that state whenever a batch's
`sequence` has a hole or carries an `events.gap`:

```sh
openemail domains webhook set acme.example --url https://…
# The changes feed is REST (`GET /mailboxes/:id/messages/changes`); the CLI
# has no wrapper for it yet, so call it with your key:
for id in $(openemail mailboxes list --json | jq -r '.mailboxes[].id'); do
  curl -s -H "Authorization: Bearer $OE_KEY" \
    "$OE_BASE/api/v1/mailboxes/$id/messages/changes" | jq -r '.state'   # keep per mailbox
done
# later, on a gap: …/messages/changes?since=$saved_state
```

## Scripting with a system key (operator)

```sh
export OPENEMAIL_API_KEY=oek_system_…
openemail admin verify-login alice@example.com --password …   # check IMAP/SMTP creds
openemail admin reindex <mailboxId>                            # rebuild the FTS index
openemail --json api GET /accounts | jq '.accounts[].id'      # any route + jq
```

## "Why is mail to this address not going out?" (operator)

```sh
openemail admin suppressions get bounced@example.com   # 404 here means clear to send
openemail admin suppressions list --all                 # the whole do-not-send list
openemail admin suppressions lift bounced@example.com   # once the cause is fixed
```

The suppression list is **deployment-global**: an address on it is refused for
every account, because the evidence is a receiver's own hard bounce or spam
complaint rather than a per-tenant setting. Rows are normally written by the
feedback-loop consumer and the relay's own in-session bounce writer; `add` is
the operator's stand-in for a complaint the consumer could not prove. `get` on
a clear address is a normal "not suppressed" answer, not a failure.

Lift with care. A hard bounce that is still a hard bounce simply re-suppresses on
the next attempt, and a lifted complaint means mailing someone who asked you to
stop — the exact thing that damages a sending reputation. The prompt shows the
recorded reason and diagnostic first; `--yes` skips it.

Tenants have their own, separate answer to the same question. An **account's
own do-not-send list** (`openemail suppressions …`, no system key needed) holds
addresses that account decided never to mail, and its `remove` also lifts a
platform **hard-bounce** block on the address — the self-healing case an
account key is allowed. A platform **complaint** row stays operator-only:

```sh
openemail suppressions add them@example.net --note "unsubscribed"
openemail suppressions check them@example.net
openemail suppressions remove them@example.net
```

## Stop a tenant that is spamming (operator)

```sh
openemail accounts send-usage ACC_01J…                 # how much have they spent today?
openemail accounts traffic ACC_01J…                    # is the volume real, and where?
openemail domains events example.com --outcome relayed # which mailbox is sending it
openemail accounts update ACC_01J… --pause             # hold everything while you look
openemail accounts update ACC_01J… --freeze            # or stop it outright
openemail accounts update ACC_01J… --resume            # once it is sorted out
```

**Reach for `--pause` first.** It refuses submissions temporarily (429 → a 451
over SMTP, so the sender's own MTA holds the mail) and DEFERS the queued backlog
instead of bouncing it, so nothing is destroyed while you investigate. `--freeze`
is the abuse stop: permanent (403 → 550) and the backlog is bounced. A hold can
be upgraded to a freeze; bounced mail cannot be recalled.

Start at the ACCOUNT scope, because it is the only view that can see this shape.
Per-mailbox and per-domain surfaces are keyed one at a time, so a tenant
spreading volume across fifty mailboxes looks healthy fifty times over while the
account total is fifty times what anyone intended. `send-usage` is the cheaper
of the two and answers "how much of their allowance is gone"; `traffic` shows
where it went.

If the answer is "they are legitimately busy", the fix is a bigger number rather
than a freeze:

```sh
openemail accounts update ACC_01J… --send-msgs-per-day 20000
openemail accounts update ACC_01J… --send-rcpts-per-day unlimited
```

Note `unlimited` and `default` are different words on purpose: `default` drops
the override and inherits the platform number, which may well be tighter than
what you just removed.

Two independent choices: **scope** (how much) and **mode** (how permanent).

Scope is scope, not strength — all three cover the same ground at different
widths, and all three reach the queued backlog:

| scope | stop | hold |
|---|---|---|
| one mailbox | `mailboxes update <id> --freeze` | `mailboxes update <id> --pause` |
| the whole tenant | `accounts update <id> --freeze` | `accounts update <id> --pause` |
| one domain | `domains update <d> --can-send=false` | `domains update <d> --send-paused` |

Reach for the ACCOUNT scope when you do not yet know how many mailboxes are
involved: it covers mailboxes the tenant creates *after* you act, which a loop
over the mailboxes they hold right now does not. It is one call, so nothing races
the tenant's own creates.

Mode is the question "should this mail survive?":

- **`--pause`** — non-payment, or a suspicion you expect to clear. Submissions
  answer 429 and the sending MTA queues; the relay defers its backlog. Nothing
  is destroyed. One caveat: a hold outliving the relay's ~3.2-day retry window
  dead-letters that backlog — it is preserved and redrivable, but no longer
  automatic, so resolve long holds before then.
- **`--freeze`** — abuse. Submissions answer 403 and the client gives up; the
  backlog is bounced. This is what you want for a spammer, whose client should
  stop retrying against you.

Both are **send-only**. A disabled or held account keeps receiving mail and every
mailbox stays readable — which is what makes either safe to use before you have
finished the investigation. Neither expires on its own; `--resume`/`--unfreeze`
when you are done, and the very next submission goes through (there is no cache
to wait out).

Revoking credentials is the *narrower* tool, not the faster one: it stops the
credential, so nothing new can be submitted with it, but it cannot reach mail
that is already queued. Use it when one app password leaked; freeze or pause
when the tenant is the problem.

## Check what is signing outbound mail (operator)

```sh
openemail admin dkim status                        # generations, active selector, alarm
openemail admin dkim status --domain example.com   # paste-ready customer CNAME rows
openemail admin dkim rotate                        # stage the next key
openemail admin dkim activate                      # flip early, once DNS resolves
```

One shared key signs all outbound mail, with `d=` set to each sender's own domain
so DMARC still aligns; customers publish two CNAMEs once and never touch DNS
again, because rotation happens on our side by alternating selectors. The cycle
is automatic (generate + publish every 30 days, 7-day soak, then flip) — these
commands observe it, and `rotate`/`activate` only make it happen sooner.

`rotate` does not change what is signing today: the current key keeps signing
through the soak. On a deployment with no keys at all it bootstraps the first
one, active immediately. A `rotate` that races the scheduler answers `409
rotation_in_progress`, which is the scheduler having got there first rather than
an error to work around.

`activate` is refused with `dkim_dns_not_ready` while the staged TXT is not
resolver-visible. `--force` skips that check and signs with a key receivers may
not be able to fetch — every message sent before the record propagates fails DKIM
and DMARC with it. Use it only when you know the DoH view is stale rather than
the record missing.

Unset config (`DKIM_ZONE_ID` / `DKIM_DNS_ROOT` / `CF_DNS_API_TOKEN` / `DKIM_KEK`)
leaves the whole feature inert: `status` says so plainly, and mail relays
unsigned rather than deferring.
