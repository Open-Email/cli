# Authentication, config & profiles

## The login model

There is no account password and no OAuth device flow in core today. Instead the
CLI uses **paste-then-self-mint**:

1. `openemail login` prompts for an API key (or takes `--api-key` /
   `OPENEMAIL_API_KEY`).
2. If it's an **account** key (`oek_…`, role account), the CLI immediately calls
   `POST /api-keys` with the pasted key to mint its **own** per-device key
   (named `openemail-cli @ <hostname>`), stores that in the OS keychain, records
   the profile, and **discards the pasted key**. Per-device named keys make
   `openemail keys list` legible and revocation surgical.
3. If it's a **system** key, it's stored as-is (minting another system key would
   itself require system role) and the profile is marked `role = system`, which
   unlocks the `admin` group.
4. `openemail logout` revokes the CLI's own key (`DELETE /api-keys/:id`) and
   deletes the stored secret.

`whoami` shows the resolved principal, account, key, default mailbox, and where
the key is stored. Identity is discovered by probing (`GET /api-keys` reveals the
tenant; `GET /accounts` distinguishes system from account) — core has no
`whoami` endpoint yet.

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
