package cli

import "testing"

// The two spam-training verbs take MANY ids, because core has a batch learn
// route and a per-message loop against it is N reads and N background budgets
// where one would do.
//
// Pinned on the arg validator rather than by running the command: the widening
// from ExactArgs(1) is the whole change here, and a revert to it is silent
// otherwise — the single-id form keeps working, so nothing else fails.
func TestLearnCommandsTakeManyIds(t *testing.T) {
	for _, verb := range []struct{ use, class string }{{"junk", "spam"}, {"not-junk", "ham"}} {
		cmd := newMessageLearnCmd(&app{}, verb.use, verb.class)
		if cmd.Args == nil {
			t.Fatalf("%s: no arg validator", verb.use)
		}
		ids := func(n int) []string {
			out := make([]string, n)
			for i := range out {
				out[i] = "01ID"
			}
			return out
		}
		for _, n := range []int{1, 2, 200} {
			if err := cmd.Args(cmd, ids(n)); err != nil {
				t.Errorf("%s: %d id(s) must be accepted: %v", verb.use, n, err)
			}
		}
		// Zero is a usage error, and past core's BATCH_MAX_IDS the server would
		// refuse the whole call — better to say so before spending the round trip.
		if err := cmd.Args(cmd, ids(0)); err == nil {
			t.Errorf("%s: no ids must be refused", verb.use)
		}
		if err := cmd.Args(cmd, ids(201)); err == nil {
			t.Errorf("%s: more than core's 200-id ceiling must be refused up front", verb.use)
		}
	}
}
