package coreapi

// Drift contract test (Step 3 of the API contract-hardening plan). It pins every
// hand-written wire struct to openemail-core's committed openapi.snapshot.json:
// for each struct↔component pairing it reflects the struct's json fields and
// compares them to the component's properties, failing on
//   (a) a spec property with no matching Go field  (core added a field),
//   (b) a Go field absent from the spec            (client expects a phantom),
//   (c) a kind mismatch (string vs number vs …),
//   (d) a nullable spec property mapped to a non-nilable Go type (a null would
//       fail to decode).
// It resolves allOf (extend), oneOf/anyOf (unions → field superset), and $ref.
//
// The snapshot is vendored into this repo (openapi.snapshot.json at the module
// root) so CI is self-contained. After any response-schema change in core, run
// `npm run spec` there, then `make sync-spec` here to re-vendor it; a stale
// snapshot makes this test the drift alarm.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// pairing maps a hand struct to its snapshot component. ignoreSpec lists spec
// properties the client deliberately does not decode; ignoreGo lists Go fields
// with no spec counterpart (decoded out-of-band).
type pairing struct {
	val        any
	comp       string
	ignoreSpec []string
	ignoreGo   []string
}

func contractPairings() []pairing {
	return []pairing{
		// Directory / control plane.
		{val: Domain{}, comp: "Domain"},
		{val: DNSStatus{}, comp: "DnsStatus"},
		{val: Route{}, comp: "Route"},
		{val: RouteMember{}, comp: "RouteMember"},
		{val: RouteMemberBatchResult{}, comp: "RouteMemberBatchResult"},
		{val: Pattern{}, comp: "Pattern"},
		{val: Account{}, comp: "Account"},
		{val: APIKey{}, comp: "ApiKey"},
		{val: CreatedAPIKey{}, comp: "ApiKeyCreated"},
		// Mailbox maps to the stats superset (the single hand struct carries the
		// GET-only stats as omitempty pointers).
		{val: Mailbox{}, comp: "MailboxWithStats"},
		{val: DeletedMailbox{}, comp: "DeletedMailbox"},
		{val: MailboxDeleteResult{}, comp: "MailboxDeleteResult"},
		{val: MailboxPurgeResult{}, comp: "MailboxPurgeResult"},
		{val: MailboxRestoreResult{}, comp: "MailboxRestoreResult"},
		{val: Credential{}, comp: "Credential"},
		{val: CreatedCredential{}, comp: "CredentialCreated"},
		{val: VerifyResult{}, comp: "VerifyResult"},
		{val: PickupSource{}, comp: "PickupSource"},
		{val: DomainTraffic{}, comp: "DomainTraffic"},
		{val: AccountTraffic{}, comp: "AccountTraffic"},
		{val: AccountSendUsage{}, comp: "AccountSendUsage"},
		{val: TrafficRow{}, comp: "TrafficRow"},
		{val: TrafficEvent{}, comp: "TrafficEvent"},
		{val: DomainEvents{}, comp: "DomainEvents"},
		// DMARC aggregate-report views. DmarcTotals has no named component
		// (core inlines it under DomainDmarc.totals) — TestDmarcTotalsMatchesInlineSchema
		// pins it separately.
		{val: DomainDmarc{}, comp: "DomainDmarc"},
		{val: DmarcReadiness{}, comp: "DmarcReadiness"},
		{val: DmarcBlocker{}, comp: "DmarcBlocker"},
		{val: DmarcSource{}, comp: "DmarcSource"},
		{val: DomainDmarcSources{}, comp: "DomainDmarcSources"},
		{val: DomainDmarcReports{}, comp: "DomainDmarcReports"},
		{val: DmarcReport{}, comp: "DmarcReport"},
		// Data plane.
		{val: MessageLabel{}, comp: "MessageLabel"},
		{val: MessageMeta{}, comp: "MessageMeta"},
		{val: ExpungedMessageMeta{}, comp: "ExpungedMessageMeta"},
		{val: LabelInfo{}, comp: "LabelInfo"},
		{val: CreatedLabel{}, comp: "CreatedLabel"},
		{val: ExpungedUID{}, comp: "ExpungedUID"},
		{val: LabelUidEntry{}, comp: "LabelUidEntry"},
		{val: MimeEntry{}, comp: "MimeEntry"},
		// Structured content view (GET .../content).
		{val: ContentResult{}, comp: "ContentResult"},
		{val: ContentHeaders{}, comp: "ContentHeaders"},
		{val: ContentBody{}, comp: "ContentBody"},
		{val: AttachmentRef{}, comp: "AttachmentRef"},
		{val: ContentAddress{}, comp: "ContentAddress"},
		{val: ThreadListItem{}, comp: "ThreadListItem"},
		{val: ReplyContext{}, comp: "ReplyContext"},
		// GetThread decodes the top-level `cursor` in an anonymous wrapper, not on
		// ThreadView itself.
		{val: ThreadView{}, comp: "ThreadView", ignoreSpec: []string{"cursor"}},
		{val: AppendResult{}, comp: "AppendResult"},
		{val: PatchResult{}, comp: "MessageUpdateResult"},
		{val: DeleteResult{}, comp: "MessageDeleteResult"},
		{val: RestoreResult{}, comp: "MessageRestoreResult"},
		{val: BatchRestoreResult{}, comp: "BatchRestoreResult"},
		{val: BatchRestoreEntry{}, comp: "BatchRestoreEntry"},
		{val: SieveScript{}, comp: "SieveScript"},
		{val: SieveScriptBody{}, comp: "SieveScriptBody"},
		{val: SievePutResult{}, comp: "SievePutResult"},
		{val: SieveCheckResult{}, comp: "SieveCheckResult"},
		{val: SieveCapabilities{}, comp: "SieveCapabilities"},
		{val: Vacation{}, comp: "Vacation"},
		// Staged uploads + the structured send.
		{val: UploadResult{}, comp: "UploadResult"},
		{val: SendResult{}, comp: "SendResult"},
		{val: SendRecipientResult{}, comp: "SendRecipientResult"},
		// Operator surfaces (system-only).
		{val: Suppression{}, comp: "Suppression"},
		{val: AccountSuppression{}, comp: "AccountSuppression"},
		{val: DkimStatus{}, comp: "DkimStatus"},
		{val: DkimKey{}, comp: "DkimKey"},
		{val: DkimCname{}, comp: "DkimCname"},
		{val: DkimRotated{}, comp: "DkimRotated"},
		// Structured search. EmailSearchRequest/Filter are free-form on the wire
		// (additionalProperties), so only the response shapes are pinned.
		{val: EmailSearchResult{}, comp: "EmailSearchResult"},
		{val: EmailSearchSnippet{}, comp: "EmailSearchSnippet"},
		// JSON filter rules (the flat authoring surface over Sieve).
		{val: FilterRule{}, comp: "FilterRule"},
		{val: RuleCondition{}, comp: "RuleCondition"},
		{val: RuleAction{}, comp: "RuleAction"},
		{val: RulesDocument{}, comp: "RulesDocument"},
		{val: FilterRulesState{}, comp: "FilterRulesState"},
		{val: FilterRulesPutResult{}, comp: "FilterRulesPutResult"},
		{val: FilterRulesDeleted{}, comp: "FilterRulesDeleted"},
		// Identities & auth introspection.
		{val: Identity{}, comp: "Identity"},
		{val: IdentityFacets{}, comp: "IdentityFacets"},
		{val: WhoamiResult{}, comp: "WhoamiResult"},
		// PIM (calendars/addressbooks). PimObject adds the listing-only content /
		// instances / data keys the spec declares inline on PimObjectPage's items.
		{val: PimCollection{}, comp: "PimCollection"},
		{val: PimObject{}, comp: "PimObjectMeta", ignoreGo: []string{"content", "instances", "data"}},
		{val: PimObjectPage{}, comp: "PimObjectPage"},
		{val: PimInstance{}, comp: "PimInstance"},
		{val: PimAttendee{}, comp: "PimAttendee"},
		{val: PimChanges{}, comp: "PimChanges"},
		{val: PimChangedRef{}, comp: "PimChangedRef"},
		{val: PimPutResult{}, comp: "PimPutResult"},
		{val: PimImportItem{}, comp: "PimImportItem"},
		{val: PimImportResult{}, comp: "PimImportResult"},
		{val: PimShare{}, comp: "PimShare"},
		{val: PimSharedWithMe{}, comp: "PimSharedWithMe"},
		{val: PimPublicCollection{}, comp: "PimPublicCollection"},
		{val: PimToken{}, comp: "PimToken"},
		{val: PimTokenCreated{}, comp: "PimTokenCreated"},
		{val: PimObjectJSON{}, comp: "PimObjectJson"},
		{val: PimInvitationStatus{}, comp: "PimInvitationStatus"},
		{val: MessageInvitation{}, comp: "MessageInvitation"},
		{val: PimRsvpResult{}, comp: "PimRsvpResult"},
		{val: Prefs{}, comp: "Prefs"},
	}
}

func TestWireStructsMatchOpenAPISnapshot(t *testing.T) {
	spec := loadSnapshot(t)
	schemas, ok := spec["components"].(map[string]any)["schemas"].(map[string]any)
	if !ok {
		t.Fatal("snapshot has no components.schemas")
	}
	for _, p := range contractPairings() {
		p := p
		t.Run(p.comp, func(t *testing.T) {
			comp, ok := schemas[p.comp].(map[string]any)
			if !ok {
				t.Fatalf("component %q not found in snapshot", p.comp)
			}
			props := resolveComponent(t, schemas, comp)
			goFields := flattenGoFields(reflect.TypeOf(p.val))
			for _, issue := range compare(p, schemas, props, goFields) {
				t.Error(issue)
			}
		})
	}
}

// ── Go reflection ────────────────────────────────────────────────────────────

type goField struct {
	kind    string // string|integer|number|boolean|array|object|any
	nilable bool
}

func flattenGoFields(t reflect.Type) map[string]goField {
	out := map[string]goField{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if f.Anonymous && tag == "" {
			for k, v := range flattenGoFields(f.Type) {
				if _, seen := out[k]; !seen {
					out[k] = v
				}
			}
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		kind, nilable := goKind(f.Type)
		out[name] = goField{kind: kind, nilable: nilable}
	}
	return out
}

func goKind(t reflect.Type) (string, bool) {
	nilable := false
	if t.Kind() == reflect.Ptr {
		nilable = true
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string", nilable
	case reflect.Bool:
		return "boolean", nilable
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer", nilable
	case reflect.Float32, reflect.Float64:
		return "number", nilable
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 { // []byte / json.RawMessage: opaque
			return "any", true
		}
		return "array", true
	case reflect.Map:
		return "object", true
	case reflect.Struct:
		return "object", nilable
	case reflect.Interface:
		return "any", true
	default:
		return "any", nilable
	}
}

// ── Spec resolution ──────────────────────────────────────────────────────────

// resolveComponent flattens a component to its property schemas, resolving
// allOf (merge), oneOf/anyOf (field superset) and $ref.
//
// It deliberately does not track the spec's `required` set: every comparison
// this harness makes (see the four checks in the file header) applies to
// required and optional properties alike, so a required set would be built and
// never read. Adding a required-vs-`omitempty` check for the request-side
// structs would need `pairing` to model direction first.
func resolveComponent(t *testing.T, schemas map[string]any, comp map[string]any) map[string]any {
	if ref, ok := comp["$ref"].(string); ok {
		return resolveComponent(t, schemas, derefSchema(t, schemas, ref))
	}
	if allOf, ok := comp["allOf"].([]any); ok {
		props := map[string]any{}
		for _, sub := range allOf {
			for k, v := range resolveComponent(t, schemas, sub.(map[string]any)) {
				props[k] = v
			}
		}
		return props
	}
	if variants := variantList(comp); variants != nil {
		props := map[string]any{}
		for _, sub := range variants {
			sm, _ := sub.(map[string]any)
			if isNullSchema(sm) {
				continue // the `null` arm of a nullableRef — no props
			}
			for k, v := range resolveComponent(t, schemas, sm) {
				if _, seen := props[k]; !seen {
					props[k] = v
				}
			}
		}
		return props
	}
	if raw, ok := comp["properties"].(map[string]any); ok {
		return raw
	}
	return map[string]any{}
}

// specKind returns the normalized kind + nullability of a property schema,
// resolving $ref and anyOf/oneOf-with-null (nullableRef).
func specKind(schemas map[string]any, schema map[string]any) (string, bool) {
	if ref, ok := schema["$ref"].(string); ok {
		return specKind(schemas, derefSchema(nil, schemas, ref))
	}
	if variants := variantList(schema); variants != nil {
		nilable := false
		kind := "any"
		for _, v := range variants {
			vm, _ := v.(map[string]any)
			if isNullSchema(vm) {
				nilable = true
				continue
			}
			k, n := specKind(schemas, vm)
			kind = k
			nilable = nilable || n
		}
		return kind, nilable
	}
	// allOf composition → object.
	if _, ok := schema["allOf"]; ok {
		return "object", false
	}
	nilable := false
	var typ string
	switch v := schema["type"].(type) {
	case string:
		typ = v
	case []any:
		for _, e := range v {
			if s, _ := e.(string); s == "null" {
				nilable = true
			} else if s != "" {
				typ = s
			}
		}
	}
	if typ == "" {
		// No type, no ref, no composition → opaque (e.g. deliveryMeta value {}).
		return "any", nilable
	}
	return typ, nilable
}

// ── Comparison ───────────────────────────────────────────────────────────────

func compare(p pairing, schemas map[string]any, props map[string]any, goFields map[string]goField) []string {
	var issues []string
	ignoreSpec := toSet(p.ignoreSpec)
	ignoreGo := toSet(p.ignoreGo)

	specNames := make([]string, 0, len(props))
	for name := range props {
		specNames = append(specNames, name)
	}
	sort.Strings(specNames)

	for _, name := range specNames {
		if ignoreSpec[name] {
			continue
		}
		gf, present := goFields[name]
		if !present {
			issues = append(issues, fmt.Sprintf("%s: spec property %q has no Go field (core added a field?)", p.comp, name))
			continue
		}
		sk, nullable := specKind(schemas, props[name].(map[string]any))
		if !kindsCompatible(sk, gf.kind) {
			issues = append(issues, fmt.Sprintf("%s.%s: spec kind %q vs Go kind %q", p.comp, name, sk, gf.kind))
		}
		if nullable && !gf.nilable {
			issues = append(issues, fmt.Sprintf("%s.%s: spec is nullable but Go type is not nilable (a null would fail to decode)", p.comp, name))
		}
	}

	goNames := make([]string, 0, len(goFields))
	for name := range goFields {
		goNames = append(goNames, name)
	}
	sort.Strings(goNames)
	for _, name := range goNames {
		if ignoreGo[name] {
			continue
		}
		if _, ok := props[name]; !ok {
			issues = append(issues, fmt.Sprintf("%s: Go field %q absent from spec (phantom field?)", p.comp, name))
		}
	}
	return issues
}

func kindsCompatible(spec, goKind string) bool {
	if spec == "any" || goKind == "any" {
		return true
	}
	if isNumeric(spec) && isNumeric(goKind) {
		return true
	}
	return spec == goKind
}

func isNumeric(k string) bool { return k == "integer" || k == "number" }

// ── helpers ──────────────────────────────────────────────────────────────────

func variantList(schema map[string]any) []any {
	if v, ok := schema["oneOf"].([]any); ok {
		return v
	}
	if v, ok := schema["anyOf"].([]any); ok {
		return v
	}
	return nil
}

func isNullSchema(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	if s, ok := schema["type"].(string); ok {
		return s == "null"
	}
	return false
}

func derefSchema(t *testing.T, schemas map[string]any, ref string) map[string]any {
	name := ref[strings.LastIndex(ref, "/")+1:]
	s, ok := schemas[name].(map[string]any)
	if !ok {
		if t != nil {
			t.Fatalf("dangling $ref %q", ref)
		}
		return map[string]any{}
	}
	return s
}

func toSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func loadSnapshot(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(snapshotPath(t))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	return spec
}

// snapshotPath locates openapi.snapshot.json: an explicit override, then the
// copy vendored at the module root (the CI path; refresh with `make sync-spec`),
// then a sibling core checkout one level up as a dev-convenience fallback.
func snapshotPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("OPENEMAIL_OPENAPI_SPEC"); p != "" {
		return p
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file) // internal/coreapi
	candidates := []string{
		filepath.Join(dir, "..", "..", "openapi.snapshot.json"),                         // vendored at module root
		filepath.Join(dir, "..", "..", "..", "openemail-core", "openapi.snapshot.json"), // sibling core checkout
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Fatalf("openapi.snapshot.json not found (looked in %s); run `npm run spec` in core or set OPENEMAIL_OPENAPI_SPEC", strings.Join(candidates, ", "))
	return ""
}
