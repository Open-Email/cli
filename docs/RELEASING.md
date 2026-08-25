# Releasing

```
make release VERSION=v0.2.1
```

That is the whole procedure. Everything below is why it has the shape it does —
read it if the release fails, or before changing it.

## What runs

`scripts/release.sh` refuses, tags, watches, then verifies:

1. **Refuses** unless you are on `main`, the tree is clean, local matches
   `origin/main`, the tag is unused, and CI is green **for this exact commit**.
2. **Tags** annotated and pushes, which is the only trigger the release workflow
   has (`on: push: tags: v*`).
3. **Watches** the workflow and fails loudly if it does.
4. **Verifies what a user gets**: the release has assets, the Homebrew formula
   reports the new version, and one real asset's SHA-256 matches the formula.

`goreleaser` does the publishing: cross-compiled binaries, shell completions,
deb/rpm, the GitHub release with a changelog generated from commit subjects, and
the Homebrew formula pushed to `Open-Email/homebrew-tap`.

## Why each check exists

None of these are hypothetical; each one is a failure that already happened.

**CI matched on the commit SHA, not "the latest run".** v0.2.0 was nearly cut
from a `main` whose CI was red — six login tests failed on the Linux runner and
passed on macOS, because `browserLikelyAvailable` reads `DISPLAY` on Linux and
short-circuits to true on darwin. A release cut then would have shipped from a
tree nobody had actually seen pass. Matching on SHA also stops a green run for an
*older* commit vouching for this one.

**The tap token is verified before anything publishes.** goreleaser pushes the
formula **last**, so a token that cannot write the tap gives the worst outcome
available: release and artifacts go out, the formula stays on the old version,
the run ends red, and users silently keep installing the previous release. That
is precisely what v0.1.1 did, and its formula had to be published by hand. The
check now runs as the first step of the release job, so a bad token costs two
seconds and publishes nothing.

**The post-publish verification is not ceremony.** A green workflow is not proof
that the release is usable: goreleaser can succeed while the formula's checksums
disagree with its assets, and that failure appears only in somebody's
`brew install`. The script downloads one asset and compares.

## The token

`TAP_GITHUB_TOKEN` (repo secret on `Open-Email/cli`) is a PAT that can write
`Open-Email/homebrew-tap`. To regenerate:

- **Resource owner: `Open-Email`** — not a personal account. A user-owned token
  cannot reach org repositories and fails with
  `403 Resource not accessible by personal access token`, which reads like a
  scope problem and is not one.
- Repository access: only `Open-Email/homebrew-tap`.
- Permissions: **Contents: Read and write**. Nothing else.
- Expiry: fine-grained PATs default to **30 days**. Choose a long one
  deliberately — this has already expired once between releases.
- If the org gates tokens, an owner must approve it under
  Settings → Personal access tokens → Pending requests.

Then `gh secret set TAP_GITHUB_TOKEN --repo Open-Email/cli`.

`.github/workflows/tap-token-canary.yml` checks it every Monday, so the next
expiry is a failed cron rather than a half-finished release. Run it on demand
with `gh workflow run tap-token-canary.yml --repo Open-Email/cli`.

**Diagnosing the token.** The tap is a *public* repo, so a token with no grant
still receives `200` and the full repository JSON — only the `permissions` block
is missing. Absence is the signal, which is why the checks assert
`.permissions.push == true` rather than a status code. And public visibility
never removed the need for a token: reads are anonymous, writes are not.

## Both repos must stay public

`homebrew-tap` so `brew tap` can clone it anonymously, and `Open-Email/cli`
because the formula's `url` fields point at its release assets — a public tap in
front of private assets simply fails one step later. A private tap is possible,
but then every user must authenticate to install, which suits an internal tool
and not a published CLI.

## Versioning

`ldflags` injects the tag into `internal/cli.Version`, so nothing in the source
needs bumping — `openemail version` reports whatever was tagged. Untagged local
builds report `0.1.0-dev`.

Under 0.x, a new user-visible capability is a minor bump (browser login was
`v0.1.1 → v0.2.0`); fixes are patches.

## Dry run

`make snapshot` builds everything locally and publishes nothing. Worth doing
after changing `.goreleaser.yml`, since most of that config is only exercised
during a real release.
