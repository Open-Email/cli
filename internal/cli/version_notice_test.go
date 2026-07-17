package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVersionNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.3", "1.2.2", true},
		{"1.2.3", "v1.2.3", false},
		{"v2.0.0", "1.9.9", true},
		{"v1.0.0", "1.0.1", false},
		{"v1.2.0", "1.1.9", true},
		{"v1.2.3-rc1", "1.2.3", false}, // prerelease suffix dropped → equal
		{"v0.2.0", "0.1.0-dev", true},
		{"garbage", "1.0.0", false},
	}
	for _, c := range cases {
		if got := versionNewer(c.latest, c.current); got != c.want {
			t.Errorf("versionNewer(%q,%q)=%v want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestFetchLatestTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"tag_name":"v9.9.9","name":"release"}`))
	}))
	defer srv.Close()
	tag, ok := fetchLatestTag(srv.URL, 2*time.Second)
	if !ok || tag != "v9.9.9" {
		t.Fatalf("got %q ok=%v", tag, ok)
	}
}

func TestUpdateStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	path := updateStatePath()
	saveUpdateState(path, updateState{LastCheck: 123, Latest: "v1.2.3"})
	got := loadUpdateState(path)
	if got.LastCheck != 123 || got.Latest != "v1.2.3" {
		t.Fatalf("round trip: %+v", got)
	}
}
