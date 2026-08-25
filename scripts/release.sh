#!/usr/bin/env bash
#
# One command to cut a release: check, tag, push, watch, verify.
#
# The publishing itself has always been automated — goreleaser builds the
# artifacts, writes the GitHub release and pushes the Homebrew formula. What was
# not automated is everything AROUND it, which is where both previous releases
# went wrong: v0.1.1 published a release and then failed to update the tap
# (leaving users on the old formula with a green-looking tag), and v0.2.0 was
# very nearly cut from a main whose CI was red.
#
# So this refuses before it tags, and verifies after it publishes. The checks
# are the point; the tagging is three lines.
#
# Usage:  scripts/release.sh v0.2.1  [-m "optional tag message"]
set -euo pipefail

REPO="Open-Email/cli"
TAP="Open-Email/homebrew-tap"
BRANCH="main"

die() { printf '\033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
ok()  { printf '\033[32m✓\033[0m %s\n' "$*"; }
say() { printf '\n\033[1m%s\033[0m\n' "$*"; }

VERSION="${1:-}"
[ -n "$VERSION" ] || die "usage: $0 vX.Y.Z [-m message]"
shift
MESSAGE=""
while [ $# -gt 0 ]; do
  case "$1" in
    -m) MESSAGE="${2:-}"; shift 2 ;;
    *)  die "unknown argument: $1" ;;
  esac
done

[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] \
  || die "version must look like v1.2.3 (got '$VERSION')"

say "Pre-flight"

command -v gh >/dev/null || die "gh is required"
gh auth status >/dev/null 2>&1 || die "gh is not authenticated (gh auth login)"
ok "gh authenticated"

branch=$(git rev-parse --abbrev-ref HEAD)
[ "$branch" = "$BRANCH" ] || die "on '$branch'; releases are cut from '$BRANCH'"
ok "on $BRANCH"

[ -z "$(git status --porcelain)" ] || die "working tree is dirty — commit or stash first"
ok "working tree clean"

git fetch -q origin "$BRANCH"
local_sha=$(git rev-parse HEAD)
remote_sha=$(git rev-parse "origin/$BRANCH")
[ "$local_sha" = "$remote_sha" ] \
  || die "local $BRANCH ($(git rev-parse --short HEAD)) differs from origin ($(git rev-parse --short origin/$BRANCH)) — pull or push first"
ok "in sync with origin/$BRANCH"

if git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null \
   || git ls-remote --exit-code --tags origin "refs/tags/$VERSION" >/dev/null 2>&1; then
  die "tag $VERSION already exists — pick another version"
fi
ok "$VERSION is unused"

# The check that would have stopped v0.2.0 being cut from a broken tree. Matched
# on the SHA, not just "the latest run", so a green run for an OLDER commit
# cannot vouch for this one.
say "CI on $local_sha"
ci=$(gh run list --repo "$REPO" --workflow=ci.yml --branch "$BRANCH" --limit 20 \
       --json headSha,status,conclusion \
       --jq "[.[] | select(.headSha==\"$local_sha\")]
             | if length == 0 then \"missing -\" else \"\(.[0].status) \(.[0].conclusion // \"-\")\" end")
read -r status concl <<<"$ci"
[ "$status" != "missing" ] || die "no CI run found for $local_sha — push and let CI finish first"
[ "$status" = "completed" ] || die "CI for this commit is '$status' — wait for it"
[ "$concl" = "success" ] || die "CI for this commit concluded '$concl' — fix it before releasing"
ok "CI green for this exact commit"

say "Tagging $VERSION"
[ -n "$MESSAGE" ] || MESSAGE="$VERSION"
git tag -a "$VERSION" -m "$MESSAGE"
git push -q origin "$VERSION"
ok "pushed tag $VERSION"

say "Release workflow"
sleep 10
run_id=""
for _ in $(seq 1 12); do
  run_id=$(gh run list --repo "$REPO" --workflow=release.yml --limit 5 \
             --json databaseId,headBranch --jq "[.[] | select(.headBranch==\"$VERSION\")][0].databaseId // empty")
  [ -n "$run_id" ] && break
  sleep 5
done
[ -n "$run_id" ] || die "release workflow did not start for $VERSION — check Actions"
echo "  watching run $run_id …"
gh run watch "$run_id" --repo "$REPO" --exit-status >/dev/null \
  || die "release workflow FAILED — see: gh run view $run_id --repo $REPO --log-failed"
ok "release workflow succeeded"

# Everything below is what was checked by hand after v0.2.0. A green workflow is
# not proof: goreleaser can publish a release and still leave the tap stale, and
# a formula whose checksums do not match its assets fails only in `brew install`.
say "Verifying what users will actually get"

num=${VERSION#v}

assets=$(gh release view "$VERSION" --repo "$REPO" --json assets --jq '.assets | length' 2>/dev/null || echo 0)
[ "$assets" -gt 0 ] || die "no GitHub release assets for $VERSION"
ok "release published with $assets assets"

formula_version=$(curl -fsSL "https://raw.githubusercontent.com/${TAP}/main/openemail.rb" | sed -n 's/^  version "\(.*\)"/\1/p')
[ "$formula_version" = "$num" ] \
  || die "tap formula is at '$formula_version', expected '$num' — the tap push did not land (check TAP_GITHUB_TOKEN)"
ok "tap formula updated to $num"

# One real download, one real checksum: the failure that shows up as a broken
# `brew install` rather than a red build.
host_os=$(uname -s | tr '[:upper:]' '[:lower:]'); [ "$host_os" = "darwin" ] || host_os=linux
host_arch=$(uname -m); case "$host_arch" in x86_64) host_arch=amd64 ;; aarch64|arm64) host_arch=arm64 ;; esac
asset="openemail_${num}_${host_os}_${host_arch}.tar.gz"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/a.tgz" "https://github.com/${REPO}/releases/download/${VERSION}/${asset}" \
  || die "could not download $asset"
if command -v sha256sum >/dev/null; then actual=$(sha256sum "$tmp/a.tgz" | awk '{print $1}')
else actual=$(shasum -a 256 "$tmp/a.tgz" | awk '{print $1}'); fi
expected=$(curl -fsSL "https://raw.githubusercontent.com/${TAP}/main/openemail.rb" \
  | grep -A2 "${asset}" | sed -n 's/.*sha256 "\(.*\)".*/\1/p' | head -1)
[ -n "$expected" ] || die "no sha256 for $asset in the formula"
[ "$actual" = "$expected" ] || die "checksum mismatch for $asset — formula $expected, asset $actual"
ok "checksum matches for $asset"

say "$VERSION is live"
echo "  brew upgrade open-email/tap/openemail"
echo "  https://github.com/${REPO}/releases/tag/${VERSION}"
