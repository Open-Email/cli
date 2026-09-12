package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// Positional arguments are checked for SHAPE before a request leaves the
// machine. Cobra already checks the count; the kind is read off the
// placeholder names in each command's Use line — `<mailboxId>`, `<domain>`,
// `<address>` — so the help text and the check cannot disagree, and a new
// command is covered by naming its argument honestly. TestArgPlaceholdersAreClassified
// walks the whole tree and fails on a placeholder this table has no opinion on.
//
// Why bother, when core validates everything: core's answer to an id that
// cannot exist is the same bare 404 it gives a real id the key may not see —
// deliberately, so a 404 leaks nothing. `openemail mailboxes get alice` then
// reads "not_found (HTTP 404)", which is true and useless. The shape of an id
// is not secret, so the CLI can say "that is not a mailbox id" for free, before
// the round trip, with an exit code (2) that tells a script it was the caller's
// input rather than the platform's state.

// argKind is what a placeholder promises about the value.
type argKind int

const (
	kindFree    argKind = iota // anything: names, hrefs, tokens, paths
	kindULID                   // a platform-minted id: 26 Crockford base32 chars
	kindInt                    // a decimal integer
	kindDomain                 // a bare hostname: no '@', at least one dot
	kindAddress                // user@domain
	kindULIDOrAddress
)

// argKinds maps a placeholder name (as it appears between < and > in Use) to
// its kind. Every placeholder in the tree must be here — free-form ones
// included, so that "unchecked" is a decision rather than an omission.
var argKinds = map[string]argKind{
	// platform-minted ids (core's ulid())
	"mailboxId":    kindULID,
	"messageId":    kindULID,
	"accountId":    kindULID,
	"account-id":   kindULID,
	"apiKeyId":     kindULID,
	"credentialId": kindULID,
	"pickupId":     kindULID,
	"identityId":   kindULID, // "the same ULID the mailbox API uses"
	// integers
	"patternId": kindInt,
	"position":  kindInt,
	"days":      kindInt,
	// names on the wire
	"domain":        kindDomain,
	"address":       kindAddress,
	"memberAddress": kindAddress,
	"member":        kindAddress, // group-route members are addresses
	// unions — anything that goes through resolveMailbox
	"address|id":        kindULIDOrAddress,
	"mailboxId|address": kindULIDOrAddress,
	"shareeMailbox":     kindULIDOrAddress,
	"granteeMailbox":    kindULIDOrAddress,
	// free-form: user-chosen names, opaque tokens, enums core validates, or
	// values whose kind depends on another argument
	"name": kindFree, "newName": kindFree, "new-name": kindFree,
	"label": kindFree, "from": kindFree, "to": kindFree,
	"list-id": kindFree, "pattern": kindFree,
	"threadId": kindFree, "calendar": kindFree, "href": kindFree, "uid": kindFree,
	"title": kindFree, "section": kindFree, "publicId": kindFree, "token-or-url": kindFree,
	"service": kindFree, "hostname": kindFree, "code": kindFree,
	"username": kindFree, "id": kindFree, "METHOD": kindFree, "path": kindFree,
	"addressbook": kindFree, "newHref": kindFree, "tokenId": kindFree, "query": kindFree,
	"key=value": kindFree,
	"accepted|declined|tentative|needs-action": kindFree,
	"domain|mailbox|account":                   kindFree,
}

// ulidAlphabet is Crockford base32 as core mints it: upper-case, no I, L, O, U.
const ulidAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// isULID reports whether s has the exact shape of an id core mints. Case
// matters: core looks ids up byte-for-byte, so a lower-cased one is a
// different (nonexistent) key, and saying so is the whole point.
func isULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(ulidAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// looksLikeAddress is the loosest honest test: a local part, one '@', and a
// domain with a dot. Core owns the real grammar.
func looksLikeAddress(s string) bool {
	at := strings.LastIndexByte(s, '@')
	return at > 0 && at < len(s)-1 && looksLikeDomain(s[at+1:])
}

// looksLikeDomain accepts what a hostname can be and nothing that is
// obviously something else: no '@', no '/', no spaces, at least one dot, no
// empty labels. Punycode and mixed case pass; core lower-cases.
func looksLikeDomain(s string) bool {
	if s == "" || strings.ContainsAny(s, "@/ \t") || !strings.Contains(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" {
			return false
		}
	}
	return true
}

// checkArg validates one value against a kind, naming the placeholder in the
// message so the user can see which argument is wrong without counting.
func checkArg(placeholder, value string, kind argKind) error {
	switch kind {
	case kindULID:
		if isULID(value) {
			return nil
		}
		if looksLikeAddress(value) {
			return usageError(fmt.Errorf("<%s> wants an id, and %q is an address — ids are 26 upper-case characters, like 01KXRS3SHN1N35G4YETVADSN0R", placeholder, value))
		}
		if up := strings.ToUpper(value); isULID(up) {
			return usageError(fmt.Errorf("<%s> %q is lower-case; ids are looked up exactly as minted — did you mean %s?", placeholder, value, up))
		}
		return usageError(fmt.Errorf("<%s> %q is not an id — ids are 26 upper-case characters, like 01KXRS3SHN1N35G4YETVADSN0R", placeholder, value))
	case kindInt:
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return usageError(fmt.Errorf("<%s> must be a number, got %q", placeholder, value))
		}
	case kindDomain:
		if looksLikeDomain(value) {
			return nil
		}
		if looksLikeAddress(value) {
			return usageError(fmt.Errorf("<%s> wants a domain name, and %q is an address — did you mean %s?", placeholder, value, value[strings.LastIndexByte(value, '@')+1:]))
		}
		return usageError(fmt.Errorf("<%s> %q is not a domain name (expected something like example.com)", placeholder, value))
	case kindAddress:
		if !looksLikeAddress(value) {
			if isULID(value) {
				return usageError(fmt.Errorf("<%s> wants an email address, and %q is an id", placeholder, value))
			}
			return usageError(fmt.Errorf("<%s> %q is not an email address (expected user@domain)", placeholder, value))
		}
	case kindULIDOrAddress:
		if isULID(value) || looksLikeAddress(value) {
			return nil
		}
		if up := strings.ToUpper(value); isULID(up) {
			return usageError(fmt.Errorf("<%s> %q is lower-case; ids are looked up exactly as minted — did you mean %s?", placeholder, value, up))
		}
		return usageError(fmt.Errorf("<%s> %q is neither an id (26 upper-case characters) nor an address (user@domain)", placeholder, value))
	}
	return nil
}

// positional is one slot of a command's positional signature.
type positional struct {
	name     string
	kind     argKind
	variadic bool
}

// usePositionals reads the positional signature off a Use line. Only tokens
// wrapped in <> or [] count; a placeholder that follows a --flag token is that
// flag's value and is skipped, and a parenthesised group of flags is skipped
// whole. `[x...]` marks the tail variadic. Placeholders the table does not
// know are reported so the test can fail on them.
func usePositionals(use string) (slots []positional, unknown []string) {
	fields := strings.Fields(use)
	if len(fields) == 0 {
		return nil, nil
	}
	fields = fields[1:] // the command name
	inParens := false
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if inParens {
			if strings.HasSuffix(f, ")") {
				inParens = false
			}
			continue
		}
		if strings.HasPrefix(f, "(") {
			inParens = !strings.HasSuffix(f, ")")
			continue
		}
		if strings.HasPrefix(f, "-") {
			i++ // its value
			continue
		}
		optional := strings.HasPrefix(f, "[")
		if !optional && !strings.HasPrefix(f, "<") {
			continue
		}
		// `[x...]` and `<x>...` both mark a variadic tail.
		variadic := strings.HasSuffix(f, "...") || strings.HasSuffix(f, "...]")
		name := strings.Trim(strings.TrimSuffix(strings.Trim(f, "<>[]"), "..."), "<>[]")
		kind, ok := argKinds[name]
		if !ok {
			unknown = append(unknown, name)
		}
		slots = append(slots, positional{name: name, kind: kind, variadic: variadic})
	}
	return slots, unknown
}

// checkArgKinds is the pre-run hook: every positional the command received is
// checked against the slot it fills. Runs before preRun, so nothing here may
// touch config or the network.
func checkArgKinds(cmd *cobra.Command, args []string) error {
	slots, _ := usePositionals(cmd.Use)
	if len(slots) == 0 {
		return nil
	}
	for i, v := range args {
		var slot positional
		switch {
		case i < len(slots):
			slot = slots[i]
		case slots[len(slots)-1].variadic:
			slot = slots[len(slots)-1]
		default:
			return nil // more args than slots: cobra's Args already ruled
		}
		if err := checkArg(slot.name, v, slot.kind); err != nil {
			return err
		}
	}
	return nil
}
