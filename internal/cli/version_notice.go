package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

// releasesURL is the GitHub latest-release endpoint; overridable in tests.
var releasesURL = "https://api.github.com/repos/Open-Email/cli/releases/latest"

const updateCheckInterval = 24 * time.Hour

type updateState struct {
	LastCheck int64  `json:"lastCheck"`
	Latest    string `json:"latest"`
}

// maybeNotifyUpdate prints a one-line "newer version available" notice to stderr,
// at most once per 24h, TTY-only, suppressible via OPENEMAIL_NO_UPDATE_NOTIFIER.
// There is no self-update — package managers own the binary. It never blocks for
// long (the network probe is capped) and never affects the command's exit code.
func (a *app) maybeNotifyUpdate() {
	if os.Getenv("OPENEMAIL_NO_UPDATE_NOTIFIER") != "" {
		return
	}
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return
	}
	if strings.Contains(Version, "dev") {
		return // don't nag from a local dev build
	}
	path := updateStatePath()
	if path == "" {
		return // cannot determine state path safely
	}
	st := loadUpdateState(path)
	if time.Since(time.Unix(st.LastCheck, 0)) >= updateCheckInterval {
		if latest, ok := fetchLatestTag(releasesURL, 1200*time.Millisecond); ok {
			st.Latest = latest
		}
		st.LastCheck = time.Now().Unix()
		saveUpdateState(path, st)
	}
	if st.Latest != "" && versionNewer(st.Latest, Version) {
		p := a.out
		if p == nil {
			p = newPrinter(false, a.flagNoColor)
		}
		fmt.Fprintf(os.Stderr, "\n%s a new release of openemail is available: %s → %s\n",
			p.paint(colYellow, "update:"), Version, st.Latest)
		fmt.Fprintln(os.Stderr, "  upgrade with your package manager (e.g. `brew upgrade openemail`).")
	}
}

func updateStatePath() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			base = filepath.Join(home, ".local", "state")
		}
	}
	if base == "" {
		return ""
	}
	return filepath.Join(base, "openemail", "version-check.json")
}

func loadUpdateState(path string) updateState {
	var st updateState
	if path == "" {
		return st
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

func saveUpdateState(path string, st updateState) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}

// fetchLatestTag GETs the GitHub latest-release tag, bounded by timeout.
func fetchLatestTag(url string, timeout time.Duration) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "openemail-cli/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", false
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false
	}
	return strings.TrimSpace(body.TagName), body.TagName != ""
}

// versionNewer reports whether latest is a strictly higher semver than current.
// Leading 'v' and any pre-release/build suffix are ignored; unparsable parts sort
// as 0. A dev/unparsable current never triggers a notice (guarded upstream).
func versionNewer(latest, current string) bool {
	lv := parseSemver(latest)
	cv := parseSemver(current)
	for i := 0; i < 3; i++ {
		if lv[i] != cv[i] {
			return lv[i] > cv[i]
		}
	}
	return false
}

func parseSemver(s string) [3]int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	// Drop any -prerelease / +build suffix.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	var out [3]int
	for i, part := range strings.SplitN(s, ".", 3) {
		if i > 2 {
			break
		}
		n, _ := strconv.Atoi(part)
		out[i] = n
	}
	return out
}
