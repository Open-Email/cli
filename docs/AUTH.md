# Authentication, config & profiles

## The login model

`openemail login` **opens a browser**. You approve the request in the console,
which mints a key for the organization you choose and hands it back over a
channel the CLI opened itself. Nothing is pasted, and no account-wide credential
passes through a clipboard.

```
openemail login              # browser (or a device code where there is no browser)
openemail login --device     # force the device code
openemail login --paste      # the old way: paste a key you already hold
OPENEMAIL_API_KEY=oek_… openemail login    # CI, unattended
```

Two flows, chosen automatically:

- **Loopback redirect** (RFC 8252 §7.3 + PKCE). The CLI binds an ephemeral port
  on `127.0.0.1`, opens `<console>/cli?…` — and prints the URL too, since a
  browser opened on the wrong machine is no use — and waits. Approval redirects
  back to that port with a one-time code, which the CLI exchanges for the key.
  The `state` parameter binds the answer to this process; PKCE means an
  intercepted code is worthless without the verifier this process kept.
- **Device code** (RFC 8628), used automatically over SSH or with no desktop,
  and forced by `--device` / `--no-browser`. The CLI prints a short code, you
  open the console on any machine and type it in, and the CLI polls until you
  approve. The code is deliberately **not** prefilled into a link (`gh` works
  the same way): carrying it from the terminal to the browser is what proves
  you are the one who started the login, and a prefilled link would only ever
  help on the machine where the loopback flow already works.

**This is not OAuth**, deliberately: no client registration, no access/refresh
pair, no scope inside a token. What comes back is the same `oek_…` account key
core has always issued — the browser is how it is *delivered*, not a new kind of
credential. The wire format is the specs' so a real authorization server could
land behind it later without changing this CLI.

The key is named `openemail-cli @ <hostname>` and **belongs to this machine**:
re-logging in revokes the one it replaces, and `openemail logout` revokes it
(`DELETE /api-keys/:id`) before deleting the stored secret. Per-device named
keys make `openemail keys list` legible and revocation surgical.

The other paths still exist and behave as they always did:

- **A pasted or piped account key** (`--paste`, `--api-key`, `OPENEMAIL_API_KEY`)
  is used once to mint the CLI's own per-device key, which is stored; the
  supplied key is discarded. `--no-mint` stores it as-is instead.
- **A system key** is stored as-is (minting another system key would itself
  require system role) and marks the profile `role = system`, unlocking `admin`.
- **A restricted key** — one scoped to particular domains — *would* be stored as
  provided with a warning saying so, because such a key cannot mint or revoke
  anything (the `/api-keys` surface is account-tier); logout would then remove it
  locally without pretending to have revoked it. **Not live yet:** domain scoping
  is unbuilt in core, which emits `account_credentials_required` nowhere today
  (see core's `docs/api-key-scoping-design.md`), so nothing can currently produce
  such a key. The branches exist as a forward-tolerance — the day core starts
  issuing scoped keys, this CLI already explains itself instead of reporting the
  restriction as a failure.

`whoami` shows the resolved principal, account, key, default mailbox, and where
the key is stored; the classification comes from core's `GET /auth/whoami`,
never from what the console said, so role and account are always core's own
facts about the bearer.

### Where the browser goes

The console is a different host from the API (`app.open.email` vs
`api.open.email`), so it cannot be derived from `--api-url`. Resolution is
`--console-url` → `OPENEMAIL_CONSOLE_URL` → the profile's `console_url` → the
production console, **but only when the API URL is the production one too**. A
custom `--api-url` with no console URL is an error naming the flag, never a
silent authorization against production. Passing `--console-url` once records it
on the profile.

The pairing is verified, not assumed: the console advertises which core API it
fronts (`GET /api/config`), and `login` refuses a mismatch with `--api-url`
before a browser ever opens — nothing minted, nothing to clean up. Should a key
be minted anyway (a console too old to advertise up front), the redemption
response names the key's deployment and the CLI revokes it there, against the
one API that accepts the revocation, before reporting the mismatch.

### Mailbox app-passwords

A mailbox principal (`oemp_…`) has limited scope: its own messages, labels,
threads, search, sieve, pickups, events, and outbound send for its own address —
but not the directory or `admin` commands. Because core exposes no route that
reveals a mailbox principal's own id, mailbox mode needs the mailbox id supplied
(`-m <ULID>`); an address can't be resolved as a mailbox principal.

## Secret storage

By default the key lives in the OS keychain (macOS Keychain, Windows Credential
Manager, or the Secret Service on Linux). If the keychain is unavailable the CLI
falls back — **loudly**, with a printed warning — to a `0600` file under
`~/.config/openemail/credentials/`. Force the file backend with `--no-keyring` or
`OPENEMAIL_NO_KEYRING=1` (useful for CI/headless).

The config file itself (`~/.config/openemail/config.toml`) never contains a
secret — only a pointer to which backend holds it.

## Profiles

Each named profile is an independent login:

```toml
default_profile = "default"

[profiles.default]
api_url         = "https://api.open.email"
account_id      = "01…"
role            = "account"
key_storage     = "keychain"
key_id          = "01…"
key_name        = "openemail-cli @ laptop"
default_mailbox = "01…"

[profiles.staging]
api_url     = "https://api.staging.open.email"
role        = "system"
key_storage = "keychain"
```

Select one with `--profile staging` or `OPENEMAIL_PROFILE=staging`. Every setting
resolves **flag > environment > profile > default**, so a one-off
`--api-key`/`--api-url` always wins, and `OPENEMAIL_API_KEY` beats the stored
profile key but not an explicit `--api-key` flag.

## Environment variables

| Variable | Effect |
|---|---|
| `OPENEMAIL_API_KEY` | bearer token for one invocation (beats the profile) |
| `OPENEMAIL_API_URL` | deployment root |
| `OPENEMAIL_PROFILE` | active profile name |
| `OPENEMAIL_NO_KEYRING` | force the 0600-file secret backend |
| `OPENEMAIL_NO_UPDATE_NOTIFIER` | silence the daily update check |
| `NO_COLOR` | disable ANSI color |
| `XDG_CONFIG_HOME` | config directory root |
| `XDG_STATE_HOME` | state directory (update-check timestamp) |
