package tui

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDoNotSendReplacementBlocksPaginationUntilReload(t *testing.T) {
	var recreated atomic.Bool
	var requests atomic.Int32
	client, done := credClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		listID := "old"
		if recreated.Load() {
			listID = "new"
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/lists"):
			io.WriteString(w, `{"lists":[{"id":"`+listID+`","name":"Do not send","direction":"outbound","verdict":"block"}]}`)
		case strings.HasSuffix(r.URL.Path, "/lists/"+listID+"/entries"):
			if listID == "old" {
				io.WriteString(w, `{"entries":[{"pattern":"a@example.com"}],"nextCursor":"a@example.com"}`)
			} else if r.URL.Query().Get("cursor") == "" {
				io.WriteString(w, `{"entries":[{"pattern":"b@example.com"}],"nextCursor":"b@example.com"}`)
			} else {
				io.WriteString(w, `{"entries":[{"pattern":"z@example.com"}],"nextCursor":""}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not_found"}`)
		}
	})
	defer done()
	s := newScreenPane(t.Context(), &Options{Client: client}, doNotSendDesc("account"))
	s.setSize(100, 30)
	s.update(s.fetchCmd("", true)())
	applyPages := func(cmd tea.Cmd) {
		for _, msg := range collectMsgs(cmd) {
			if pg, ok := msg.(pageMsg); ok {
				s.update(pg)
			}
		}
	}
	if len(s.all) != 1 || s.next == "" {
		t.Fatal("initial page missing")
	}
	recreated.Store(true)
	applyPages(s.loadMore())
	if !strings.Contains(s.errMsg, "press r to reload") {
		t.Fatalf("missing reload warning: %q", s.errMsg)
	}
	before := requests.Load()
	// Neither explicit load-more nor automatic pagination at the bottom may
	// reuse the old cursor after the warning. Exercise repeated key presses.
	for range 2 {
		for _, key := range []tea.KeyMsg{
			{Type: tea.KeyRunes, Runes: []rune{'m'}}, {Type: tea.KeyDown},
		} {
			_, cmd := s.update(key)
			applyPages(cmd)
		}
	}
	if requests.Load() != before || len(s.all) != 1 || s.all[0].cells[0] != "a@example.com" {
		t.Fatal("pagination continued after the reload warning")
	}
	_, reload := s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	applyPages(reload)
	if s.errMsg != "" || len(s.all) != 1 || s.all[0].cells[0] != "b@example.com" {
		t.Fatalf("reload did not replace the old list: error=%q rows=%v", s.errMsg, s.all)
	}
	applyPages(s.loadMore())
	if len(s.all) != 2 || s.all[1].cells[0] != "z@example.com" {
		t.Fatal("pagination did not resume on the replacement list")
	}
}
