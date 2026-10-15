package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Open-Email/cli/internal/config"
	"github.com/Open-Email/cli/internal/secrets"
)

func TestStatusRefusesCrossDeploymentProfileToken(t *testing.T) {
	var leakedAuth atomic.Int32
	var healthAuth atomic.Int32
	otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			if r.Header.Get("Authorization") != "" {
				healthAuth.Add(1)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.Header.Get("Authorization") != "" {
			leakedAuth.Add(1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"account","accountId":"acc_1"}`))
	}))
	defer otherSrv.Close()

	// Profile has empty APIURL in config.toml (meaning default https://api.open.email),
	// and a stored profile token. Pointing --api-url at otherSrv must NOT leak the
	// stored token via `status` or `authedClient()`.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENEMAIL_PROFILE", "")
	t.Setenv("OPENEMAIL_API_URL", "")
	t.Setenv("OPENEMAIL_NO_KEYRING", "1")
	t.Setenv("OPENEMAIL_API_KEY", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := secrets.Save(cfg.ConfigDir(), "default", "oek_prod_secret", true); err != nil {
		t.Fatal(err)
	}
	// Notice APIURL is empty in Profile (defaulting to DefaultAPIURL).
	if err := cfg.SetProfile("default", config.Profile{
		Role:       "account",
		KeyStorage: secrets.File,
		KeyID:      "key_1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	a := &app{flagAPIURL: otherSrv.URL}
	if err := a.preRun(); err != nil {
		t.Fatal(err)
	}
	a.out = quietPrinter()

	// 1. authedClient must refuse even when a.profile.APIURL == ""
	if _, err := a.authedClient(); err == nil {
		t.Fatal("authedClient allowed sending a default-deployment profile token to another API URL")
	}

	// 2. status command must not leak the token on /health or /auth/whoami
	cmd := newStatusCmd(a)
	cmd.SetContext(context.Background())
	err = cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("status succeeded against a foreign --api-url while holding a profile token")
	}
	if healthAuth.Load() != 0 {
		t.Fatal("/health request carried an Authorization header")
	}
	if leakedAuth.Load() != 0 {
		t.Fatal("status leaked the stored profile token to another deployment")
	}
}

func TestAuthedClientPlainHTTPWarning(t *testing.T) {
	cases := []struct {
		name     string
		apiURL   string
		wantWarn bool
	}{
		{"remote plain http warns", "http://api.staging.test", true},
		{"localhost prefix spoof warns", "http://localhost.evil.test", true},
		{"127.0.0.1 prefix spoof warns", "http://127.0.0.1.evil.test", true},
		{"localhost loopback does not warn", "http://localhost:8787", false},
		{"uppercase LOCALHOST does not warn", "http://LOCALHOST:8787", false},
		{"127.0.0.1 loopback does not warn", "http://127.0.0.1:8787", false},
		{"ipv6 loopback does not warn", "http://[::1]:8787", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var errBuf bytes.Buffer
			a := &app{
				out:         &Printer{out: &bytes.Buffer{}, err: &errBuf},
				profileName: "default",
				profile:     config.Profile{APIURL: tc.apiURL},
				apiURL:      tc.apiURL,
				token:       "oek_test",
				tokenSource: "profile",
			}
			if _, err := a.authedClient(); err != nil {
				t.Fatalf("authedClient(%q): %v", tc.apiURL, err)
			}
			warned := strings.Contains(errBuf.String(), "plain HTTP")
			if warned != tc.wantWarn {
				t.Fatalf("apiURL %q: warned=%v, want %v (stderr=%q)", tc.apiURL, warned, tc.wantWarn, errBuf.String())
			}
		})
	}
}

func TestPreRunRejectsInvalidProfileName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENEMAIL_API_URL", "")
	t.Setenv("OPENEMAIL_PROFILE", "")

	a := &app{flagProfile: "../escape"}
	err := a.preRun()
	if err == nil {
		t.Fatal("preRun silently accepted invalid --profile '../escape'")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 2 {
		t.Fatalf("want ExitError code 2, got %v", err)
	}
}

// The warning covers every key sent over plain HTTP, and only a profile's key
// is stored: an env or flag key must not be described as one.
func TestPlainHTTPWarningDescribesTheKeyItSends(t *testing.T) {
	for _, source := range []string{"profile", "env", "flag"} {
		var errBuf bytes.Buffer
		a := &app{
			out:         &Printer{out: &bytes.Buffer{}, err: &errBuf},
			profileName: "default",
			profile:     config.Profile{APIURL: "http://api.staging.test"},
			apiURL:      "http://api.staging.test",
			token:       "oek_test",
			tokenSource: source,
		}
		if _, err := a.authedClient(); err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		got := errBuf.String()
		if !strings.Contains(got, "plain HTTP") {
			t.Fatalf("%s key over plain HTTP did not warn: %q", source, got)
		}
		if source != "profile" && strings.Contains(got, "stored") {
			t.Fatalf("%s key described as stored: %q", source, got)
		}
	}
}

// A default_profile that fails validation can only come from a hand edit of
// config.toml, and `login --profile` does not overwrite a set default, so the
// error has to say where the bad value lives.
func TestPreRunNamesAnInvalidDefaultProfileSource(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("OPENEMAIL_PROFILE", "")
	t.Setenv("OPENEMAIL_API_URL", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultProfile = "x/y"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	err = (&app{}).preRun()
	if err == nil || !strings.Contains(err.Error(), "default_profile") || !strings.Contains(err.Error(), "config.toml") {
		t.Fatalf("want an error naming default_profile in config.toml, got %v", err)
	}
}
