# openemail

The command-line client for [OpenEmail](https://open.email). Read and send mail,
manage calendars and contacts, and look after your accounts and domains. Use
individual commands in scripts or open the interactive console with `openemail ui`.

## Install

With Homebrew on macOS or Linux:

```sh
brew install Open-Email/tap/openemail
```

For Linux packages and Windows or macOS downloads, see
[Releases](https://github.com/Open-Email/cli/releases/latest). On Windows, extract
the ZIP and add the folder containing `openemail.exe` to your `PATH`.

You can also install with Go:

```sh
go install github.com/Open-Email/cli/cmd/openemail@latest
```

Check your installation with `openemail version`. Run `openemail upgrade` for
upgrade instructions, or `openemail completion <shell>` to generate completions
for Bash, Zsh, Fish, or PowerShell.

## Get started

Log in, choose a mailbox, and open the console:

```sh
openemail login
openemail mailboxes list
openemail mailboxes use alice@example.com
openemail ui
```

Login opens your browser for approval and stores a dedicated key in your OS
keychain. The console requires an account or system key.

On a remote machine or without a local browser, use a code you can approve on
another device:

```sh
openemail login --no-browser
```

Use `openemail login --paste` if you already have a key. `openemail whoami` shows
your current identity, and `openemail status` checks connectivity and authentication.

Mail commands use the mailbox selected by `mailboxes use`. Pass
`-m alice@example.com` or `-m <mailbox-id>` to choose another for one command.
Mailbox app passwords are limited to their own mailbox and require its ID for
an explicit `-m` override.

## Common tasks

Replace the example addresses, IDs, and filenames with your own.

```sh
# Read and search mail.
openemail messages list --label INBOX --limit 20
openemail messages get MESSAGE_ID
openemail search invoice
openemail search --from alice@example.com --after 7d

# Send a message, or attach a file.
openemail send --from me@example.com --to you@example.com --subject Hi --text 'Hello!'
openemail compose --from me@example.com --to you@example.com \
  --subject Report --text 'See attached.' --attach report.pdf

# Watch for new mail and calendar or contact changes. Ctrl-C stops the stream.
openemail watch

# Set an out-of-office reply.
openemail vacation on --subject Away --text 'Back on Monday.'
openemail vacation off
```

To edit a calendar event as JSON:

```sh
openemail calendars objects get work standup.ics --jscalendar -o event.json
# Edit event.json, then write it back.
openemail calendars objects put work standup.ics --jscalendar --file event.json
```

For contacts, use `addressbooks objects` and `--jscontact`. The global `--json`
option returns the full response, including metadata; it is not the same as the
editable calendar or contact document.

## Find a command

| Task | Commands |
|---|---|
| Read and organize mail | `messages`, `threads`, `search`, `labels` |
| Send messages and prepared emails | `send`, `compose`, `templates` |
| Filter, forward, or collect mail | `rules`, `sieve`, `vacation`, `forwarding`, `pickups` |
| Manage allow and block lists | `do-not-send`, `lists` |
| Work with calendars and contacts | `calendars`, `addressbooks`, `pim`, `prefs` |
| Manage accounts, domains, and addresses | `accounts`, `domains`, `mailboxes`, `identities`, `routes`, `patterns` |
| Manage keys and mailbox passwords | `keys`, `credentials` |
| Use the console, events, or API | `ui`, `watch`, `api`, `deliver` |
| Operate a deployment | `admin` (requires a system key) |

Help is available at every level:

```sh
openemail --help
openemail messages --help
openemail messages list --help
```

For step-by-step examples, see the [cookbook](docs/COOKBOOK.md), including domain
setup, filters, shared calendars, templates, and operator tasks. Available commands
and access depend on your key: account keys manage their own account, mailbox app
passwords access one mailbox, and system keys provide operator access.

## Profiles and configuration

Use named profiles for separate accounts or deployments:

```sh
openemail login --profile work
openemail messages list --profile work
```

For a self-hosted deployment, supply both `--api-url` and `--console-url` when
logging in. See the [authentication guide](docs/AUTH.md) for details.

Connection settings resolve in this order: flags, environment variables, the
selected profile, then defaults.

| Setting | Flag | Environment variable |
|---|---|---|
| Profile | `--profile` | `OPENEMAIL_PROFILE` |
| API address | `--api-url` | `OPENEMAIL_API_URL` |
| Login console | `--console-url` | `OPENEMAIL_CONSOLE_URL` |
| Key for one command | `--api-key` | `OPENEMAIL_API_KEY` |

Configuration lives in `~/.config/openemail/config.toml`, or under
`$XDG_CONFIG_HOME/openemail` when set. Keys are stored separately in the OS
keychain. If it is unavailable, the CLI warns and uses a file in `credentials/`
beside `config.toml`. Files use owner-only permissions (`0600`) on Unix; Windows
uses its file and folder access controls. Use `--no-keyring`
or `OPENEMAIL_NO_KEYRING=1` to choose file storage explicitly.

## Use in scripts

Provide a key through `OPENEMAIL_API_KEY` to run commands without an interactive
login. In CI, set it through your secret settings.

```sh
openemail mailboxes list --json --all
```

Data goes to stdout; errors, progress, and warnings go to stderr. `--json` gives
machine-readable output. Paginated commands offer `--limit` and `--cursor`; use
`--all` where supported. Some commands, including structured search, use different
pagination. See [output and exit codes](docs/OUTPUT.md) for the details.

The CLI collects no usage analytics or crash reports. Interactive runs check
GitHub for updates at most once a day; set `OPENEMAIL_NO_UPDATE_NOTIFIER=1` to
disable that check.

## Development

Use the Go version in [go.mod](go.mod) or newer.

```sh
git clone https://github.com/Open-Email/cli.git
cd cli
make build          # Build bin/openemail.
make test           # Run the CLI tests with race detection.
make lint           # Check formatting and run go vet.
```

CI runs the full CLI suite on Linux and the credential suite on Windows.
[Live tests](test/live/README.md) run against a deployed OpenEmail server;
[release instructions](docs/RELEASING.md) cover publishing a new version.

When commands or defaults change, update their help and the relevant guide. Keep
this README focused on setup and common tasks.
