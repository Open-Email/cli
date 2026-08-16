package cli

import (
	"strings"
	"testing"

	"github.com/Open-Email/cli/internal/coreapi"
)

// The listing-order flags: --order (direction) and --sort-by (which date).
// They are validated locally so a typo costs no round trip, and the DATE column
// follows --sort-by so a row's date and its position cannot disagree.

func TestValidateListOrder(t *testing.T) {
	// Empty is the important accept: it means "let core choose", which is how
	// this client avoids restating a default core owns.
	for _, tc := range []struct{ order, sortBy string }{
		{"", ""},
		{"asc", "date"},
		{"desc", "arrival"},
	} {
		if err := validateListOrder(tc.order, tc.sortBy); err != nil {
			t.Errorf("validateListOrder(%q, %q): unexpected error %v", tc.order, tc.sortBy, err)
		}
	}

	for _, tc := range []struct{ order, sortBy, want string }{
		{"newest", "", "--order"},
		{"ASC", "", "--order"}, // core's enum is case-sensitive; so is this
		{"", "sent", "--sort-by"},
		{"", "sentAt", "--sort-by"}, // the JMAP spelling is not this parameter's
		{"", "received", "--sort-by"},
	} {
		err := validateListOrder(tc.order, tc.sortBy)
		if err == nil {
			t.Errorf("validateListOrder(%q, %q): expected a refusal", tc.order, tc.sortBy)
			continue
		}
		// The message must name the accepted values — core's validation_failed
		// envelope does not, which is the whole reason this check is local.
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("validateListOrder(%q, %q) = %v; want it to name %s", tc.order, tc.sortBy, err, tc.want)
		}
	}
}

func TestListDateFollowsTheSortKey(t *testing.T) {
	sent := int64(1_700_000_000)
	m := coreapi.MessageMeta{ReceivedAt: 1_700_009_999, SentAt: &sent}

	// The case the flag exists for: a message whose claimed send time is not
	// when it arrived. Printing arrival next to a date-ordered row is the
	// mismatch being removed.
	if got := listDateOf(m, "date"); got != sent {
		t.Errorf("listDateOf(date) = %d; want the Date header %d", got, sent)
	}
	if got := listDateOf(m, "arrival"); got != m.ReceivedAt {
		t.Errorf("listDateOf(arrival) = %d; want the arrival time %d", got, m.ReceivedAt)
	}
	// Unset is core's default, arrival — never a guess at the other one.
	if got := listDateOf(m, ""); got != m.ReceivedAt {
		t.Errorf("listDateOf(\"\") = %d; want the arrival time %d", got, m.ReceivedAt)
	}
	// No Date header: core's key is COALESCE(sent_at, received_at), so the
	// column has to fall back the same way rather than print a zero epoch.
	noDate := coreapi.MessageMeta{ReceivedAt: 1_700_009_999}
	if got := listDateOf(noDate, "date"); got != noDate.ReceivedAt {
		t.Errorf("listDateOf(date) with no Date header = %d; want %d", got, noDate.ReceivedAt)
	}
}

func TestThreadActivityFollowsTheSortKey(t *testing.T) {
	sent := int64(1_700_000_500)
	item := coreapi.ThreadListItem{
		// MAX(receivedAt) over the members — the arrival-mode answer.
		LastReceivedAt: 1_700_009_999,
		// Under sortBy=date core makes the exemplar the newest-DATED member, so
		// ITS date is the position the row was sorted into.
		Exemplar: coreapi.MessageMeta{ReceivedAt: 1_700_001_000, SentAt: &sent},
	}
	if got := threadActivityAt(item, "date"); got != sent {
		t.Errorf("threadActivityAt(date) = %d; want the exemplar's sent date %d", got, sent)
	}
	if got := threadActivityAt(item, ""); got != item.LastReceivedAt {
		t.Errorf("threadActivityAt(\"\") = %d; want lastReceivedAt %d", got, item.LastReceivedAt)
	}
	// A date-sorted thread whose exemplar carries no Date header falls back to
	// that member's arrival, not to the thread's MAX — the exemplar is the row.
	item.Exemplar.SentAt = nil
	if got := threadActivityAt(item, "date"); got != item.Exemplar.ReceivedAt {
		t.Errorf("threadActivityAt(date) with no Date header = %d; want %d", got, item.Exemplar.ReceivedAt)
	}
}
