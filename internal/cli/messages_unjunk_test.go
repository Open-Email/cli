package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

func TestMessageUnjunkUniqueTargets(t *testing.T) {
	const mailboxID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const firstID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	const secondID = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	for _, tc := range []struct {
		name      string
		args      []string
		wantIDs   []string
		missing   bool
		wantError bool
		wantCount string
	}{
		{"repeated success", []string{firstID, firstID}, []string{firstID}, false, false, "Unjunked 1 message(s)"},
		{"partial success", []string{secondID, firstID, secondID, firstID}, []string{secondID, firstID}, false, true, "Unjunked 1 of 2"},
		{"missing response", []string{firstID, secondID, firstID}, []string{firstID, secondID}, true, true, "Unjunked 1 of 2"},
	} {
		for _, jsonMode := range []bool{false, true} {
			mode := "human"
			if jsonMode {
				mode = "json"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				requests := make(chan []string, 2)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || r.URL.Path != "/api/v1/mailboxes/"+mailboxID+"/messages/unjunk" {
						t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					var body struct {
						IDs []string `json:"ids"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode request: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					requests <- body.IDs
					// Core applies each explicit ID in request order. Restoring a
					// repeated ID succeeds once, then answers not_junked.
					seen := map[string]bool{}
					res := coreapi.BatchUnjunkResult{}
					for _, id := range body.IDs {
						if tc.missing && id == secondID {
							continue
						}
						entry := coreapi.BatchUnjunkEntry{ID: id, Status: "not_junked"}
						if id == firstID && !seen[id] {
							entry.Status = "unjunked"
							entry.Message = &coreapi.MessageMeta{ID: id}
						}
						seen[id] = true
						res.Results = append(res.Results, entry)
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(res)
				}))
				defer srv.Close()
				var stdout, stderr bytes.Buffer
				a := &app{apiURL: srv.URL, token: "test-token", tokenSource: "flag", flagMailbox: mailboxID,
					out: &Printer{out: &stdout, err: &stderr, json: jsonMode}}
				cmd := newMessageUnjunkCmd(a)
				cmd.SilenceErrors, cmd.SilenceUsage = true, true
				cmd.SetArgs(tc.args)
				err := cmd.Execute()
				if len(requests) != 1 {
					t.Fatalf("want one batch request, got %d (error: %v)", len(requests), err)
				}
				if got := <-requests; !reflect.DeepEqual(got, tc.wantIDs) {
					t.Errorf("request IDs = %v, want unique IDs in first-occurrence order %v", got, tc.wantIDs)
				}
				if tc.wantError {
					var exit *ExitError
					if !errors.As(err, &exit) || exit.Code != 1 {
						t.Errorf("partial failure should exit 1, got %v", err)
					}
				} else if err != nil {
					t.Errorf("unjunk failed: %v", err)
				}
				if !strings.Contains(stderr.String(), tc.wantCount) {
					t.Errorf("summary = %q, want %q", stderr.String(), tc.wantCount)
				}
				if jsonMode {
					var res coreapi.BatchUnjunkResult
					if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
						t.Fatalf("invalid JSON output: %v", err)
					}
					wantResults := len(tc.wantIDs)
					if tc.missing {
						wantResults--
					}
					if len(res.Results) != wantResults {
						t.Errorf("got %d results, want %d", len(res.Results), wantResults)
					}
				} else {
					for _, id := range tc.wantIDs {
						if strings.Count(stdout.String(), id) != 1 {
							t.Errorf("want one row for %s:\n%s", id, stdout.String())
						}
					}
					if !strings.Contains(stdout.String(), "unjunked") || (tc.missing && !strings.Contains(stdout.String(), "no answer")) {
						t.Errorf("missing per-ID outcome:\n%s", stdout.String())
					}
				}
			})
		}
	}
}

func TestMessageUnjunkHelpExplainsAutomaticLearning(t *testing.T) {
	cmd := newMessageUnjunkCmd(&app{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	help := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{"automatically schedules ham training", "undo window", "not needed"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(help, "does NOT train") || strings.Contains(help, "follow with") {
		t.Errorf("help still promises move-only behavior:\n%s", out.String())
	}
}
