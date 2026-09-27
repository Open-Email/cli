package cli

import (
	"fmt"
	"strings"

	"github.com/Open-Email/cli/internal/coreapi"
)

// A 404 from core is the bare word {error:"not_found"}: by design it does
// not say whether the thing is missing, belongs to another tenant, or lies
// outside a domain-scoped key's reach, and it does not say WHAT was looked
// for either. The CLI knows the second half — it built the path — so it says
// that, plus what a well-formed id of that kind looks like and where to list
// them. The first half stays honest: the resource may exist and be invisible.

// resourceNoun describes one collection segment of a core path.
type resourceNoun struct {
	noun string  // "mailbox", "route for address"
	list string  // the command that enumerates them, or ""
	kind argKind // the shape of its id, for the "does not look like" hint
}

// collections maps a path segment to the noun it collects. A segment not in
// here is a verb or a leaf (/send, /dns, /search) and never introduces an id.
var collections = map[string]resourceNoun{
	"mailboxes":     {"mailbox", "openemail mailboxes list", kindULID},
	"messages":      {"message", "openemail messages list", kindULID},
	"threads":       {"thread", "openemail threads list", kindFree},
	"labels":        {"label", "openemail labels list", kindFree},
	"domains":       {"domain", "openemail domains list", kindDomain},
	"accounts":      {"account", "openemail accounts list", kindULID},
	"routes":        {"route for address", "openemail routes list", kindAddress},
	"patterns":      {"pattern", "openemail patterns list <domain>", kindInt},
	"api-keys":      {"API key", "openemail keys list", kindULID},
	"credentials":   {"credential", "openemail credentials list <mailboxId>", kindULID},
	"identities":    {"identity", "openemail identities get", kindFree},
	"pickups":       {"pickup", "openemail pickups list", kindULID},
	"suppressions":  {"suppression for", "openemail do-not-send list", kindAddress},
	"hostnames":     {"hostname for service", "openemail hostnames list <domain>", kindFree},
	"members":       {"member", "openemail routes members list <address>", kindAddress},
	"lists":         {"address list", "openemail lists list", kindFree},
	"scripts":       {"Sieve script", "openemail sieve scripts list", kindFree},
	"destinations":  {"forwarding destination", "openemail forwarding show", kindFree},
	"subscriptions": {"subscription", "openemail pim subscriptions", kindFree},
	"calendars":     {"calendar", "openemail calendars list", kindFree},
	"addressbooks":  {"addressbook", "openemail addressbooks list", kindFree},
	"objects":       {"object", "", kindFree},
	"shares":        {"share", "", kindFree},
	"tokens":        {"feed token", "", kindFree},
	"entries":       {"entry", "", kindFree},
	"feeds":         {"feed", "", kindFree},
	"uploads":       {"upload", "", kindULID},
	"deliveries":    {"delivery", "", kindULID},
}

// pathLiterals are the segments core's routes spell out — a segment that
// follows a collection and is one of these is a sub-route, not an id.
// (`/mailboxes/X/messages/compose` names no message.) Labels are the one
// collection whose ids are user-chosen words, so the check is skipped there.
var pathLiterals = map[string]bool{
	"compose": true, "learn": true, "mime": true, "restore": true, "batch": true,
	"check": true, "run": true, "test": true, "all": true, "evaluate": true,
	"active": true, "capabilities": true, "expunge": true, "reply": true,
	"respond": true, "invitations": true, "purge": true, "dns": true,
	"traffic": true, "events": true, "dmarc": true, "reports": true,
	"sources": true, "send-usage": true, "send": true, "reindex": true,
	"retention": true, "webhook": true, "search": true, "query": true,
	"semantic": true, "similar": true, "changes": true, "export": true,
	"import": true, "shared-with-me": true, "public-directory": true,
	"activate": true, "rotate": true, "report": true, "trash": true,
	"uids": true, "parts": true, "content": true, "raw": true, "vacation": true,
	"prefs": true, "pim": true, "sieve": true, "forwarding": true,
	"auth": true, "deliver": true, "inbound": true, "outbound": true,
	"pickup": true, "dkim": true, "health": true, "audit": true, "usage": true,
	"scheduled": true, "junk": true, "not-junk": true, "flags": true,
}

// notFoundRef is one (collection, id) pair read off the refused path.
type notFoundRef struct {
	noun resourceNoun
	id   string
}

// notFoundRefs walks a decoded /api/v1-relative path and returns the
// resources it names, outermost first. `/mailboxes/X/messages/Y` yields
// mailbox X then message Y; `/mailboxes/X/labels` yields just mailbox X.
func notFoundRefs(path string) []notFoundRef {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	var refs []notFoundRef
	for i := 0; i < len(segs); i++ {
		noun, ok := collections[segs[i]]
		if !ok || i+1 >= len(segs) {
			continue
		}
		id := segs[i+1]
		if id == "" || (segs[i] != "labels" && (pathLiterals[id] || collections[id].noun != "")) {
			continue
		}
		refs = append(refs, notFoundRef{noun: noun, id: id})
		i++
	}
	return refs
}

// describeNotFound renders a 404 as what was asked for and why it might not
// have been there. The first string is the subject line (empty when the path
// names nothing this table knows, in which case the caller falls back to the
// method and path); the rest are hint lines.
func describeNotFound(ae *coreapi.APIError) (subject string, hints []string) {
	if ae.Path == "" {
		return "", nil
	}
	refs := notFoundRefs(ae.Path)
	if len(refs) == 0 {
		return "", []string{fmt.Sprintf("%s %s answered 404 — the route may not exist on this deployment, or the path is wrong", ae.Method, ae.Path)}
	}
	last := refs[len(refs)-1]
	subject = fmt.Sprintf("no %s %q", last.noun.noun, last.id)
	if len(refs) > 1 {
		parent := refs[len(refs)-2]
		subject += fmt.Sprintf(" in %s %q", parent.noun.noun, parent.id)
	}
	// Any level of the path can be the one that failed, and core will not say
	// which; the shape check runs over all of them so a bad parent id is named.
	for _, r := range refs {
		if h := shapeHint(r); h != "" {
			hints = append(hints, h)
		}
	}
	if last.noun.list != "" {
		hints = append(hints, "list them with: "+last.noun.list)
	}
	hints = append(hints, "(it may not exist, or this key may not be allowed to see it — core answers both the same way)")
	return subject, hints
}

// shapeHint says when an id could never have matched, which turns "not found"
// into "not an id" — the far more common mistake at a keyboard.
func shapeHint(r notFoundRef) string {
	switch r.noun.kind {
	case kindULID:
		if isULID(r.id) {
			return ""
		}
		if looksLikeAddress(r.id) {
			return fmt.Sprintf("%q is an address, but a %s is named by its id (26 upper-case characters, like 01KXRS3SHN1N35G4YETVADSN0R)", r.id, r.noun.noun)
		}
		if up := strings.ToUpper(r.id); isULID(up) {
			return fmt.Sprintf("%q is lower-case; ids are matched exactly as minted — did you mean %s?", r.id, up)
		}
		return fmt.Sprintf("%q does not look like a %s id (26 upper-case characters, like 01KXRS3SHN1N35G4YETVADSN0R)", r.id, r.noun.noun)
	case kindDomain:
		if looksLikeDomain(r.id) {
			return ""
		}
		if looksLikeAddress(r.id) {
			return fmt.Sprintf("%q is an address; the domain is %s", r.id, r.id[strings.LastIndexByte(r.id, '@')+1:])
		}
		return fmt.Sprintf("%q does not look like a domain name", r.id)
	case kindAddress:
		if looksLikeAddress(r.id) {
			return ""
		}
		return fmt.Sprintf("%q does not look like an email address (user@domain)", r.id)
	case kindInt:
		if strings.Trim(r.id, "0123456789") != "" {
			return fmt.Sprintf("%q is not a number", r.id)
		}
	}
	return ""
}
