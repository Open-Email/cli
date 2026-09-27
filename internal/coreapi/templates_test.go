package coreapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// recordedCall is one request a fake core received, body decoded.
type recordedCall struct {
	method, path, rawPath string
	header                http.Header
	body                  map[string]any
}

// templateCore answers every call with `answer(r)` and records it.
func templateCore(t *testing.T, answer func(r *http.Request) (int, string)) (*httptest.Server, *[]recordedCall) {
	t.Helper()
	var calls []recordedCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := recordedCall{method: r.Method, path: r.URL.Path, rawPath: r.URL.EscapedPath(), header: r.Header.Clone()}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			if err := json.Unmarshal(raw, &c.body); err != nil {
				t.Errorf("request body is not a JSON object: %s", raw)
			}
		}
		calls = append(calls, c)
		status, body := answer(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

const templateJSON = `{"slug":"order-shipped","name":"Order shipped","from":"orders@acme.test","replyTo":null,` +
	`"variables":{"orderRef":{"description":"The order","sample":"A-1"}},"defaultLang":"en","version":3,` +
	`"languages":["en","de"],"createdAt":1757000000,"updatedAt":1757700000}`

// The templates client (core docs/templated-mail-design.md §VI): every call is
// under the ACCOUNT the caller names, a slug or language is one escaped path
// segment, and each body carries exactly the keys core's strict schemas define.
func TestTemplatesClientPathsAndBodies(t *testing.T) {
	srv, calls := templateCore(t, func(r *http.Request) (int, string) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/accounts/ACC1/templates":
			return 200, `{"templates":[` + templateJSON + `]}`
		case r.Method == http.MethodGet:
			return 200, templateJSON[:len(templateJSON)-1] + `,"variants":[{"lang":"en","subject":"S","text":"T","html":null,"updatedAt":1}]}`
		case r.Method == http.MethodPut:
			return 200, `{"slug":"order-shipped","lang":"de","stored":true,"changed":false}`
		case r.Method == http.MethodDelete && len(r.URL.Path) > len("/api/v1/accounts/ACC1/templates/order-shipped"):
			return 200, `{"slug":"order-shipped","lang":"de","removed":true}`
		case r.Method == http.MethodDelete:
			return 200, `{"slug":"order-shipped","deleted":true}`
		default:
			return 200, templateJSON
		}
	})
	c := testClient(t, srv.URL)
	ctx := context.Background()

	list, err := c.ListTemplates(ctx, "ACC1")
	if err != nil || len(list) != 1 || list[0].Slug != "order-shipped" || list[0].Version != 3 {
		t.Fatalf("ListTemplates = %+v, %v", list, err)
	}

	if _, err := c.CreateTemplate(ctx, "ACC1", TemplateCreate{
		Slug: "order-shipped", Name: "Order shipped", From: "orders@acme.test",
		Variables: json.RawMessage(`{"orderRef":{"sample":"A-1"}}`),
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := c.GetTemplate(ctx, "ACC1", "order-shipped")
	if err != nil || len(detail.Variants) != 1 || detail.Variants[0].HTML != nil {
		t.Fatalf("GetTemplate = %+v, %v", detail, err)
	}
	if _, err := c.UpdateTemplate(ctx, "ACC1", "order-shipped", map[string]any{"replyTo": nil}); err != nil {
		t.Fatal(err)
	}
	put, err := c.PutTemplateVariant(ctx, "ACC1", "order-shipped", "de", VariantCopy{Subject: "Versandt", Text: "Hallo"})
	if err != nil || put.Changed {
		t.Fatalf("PutTemplateVariant = %+v, %v", put, err)
	}
	removed, err := c.DeleteTemplateVariant(ctx, "ACC1", "order-shipped", "de")
	if err != nil || !removed.Removed {
		t.Fatalf("DeleteTemplateVariant = %+v, %v", removed, err)
	}
	if err := c.DeleteTemplate(ctx, "ACC1", "order-shipped"); err != nil {
		t.Fatal(err)
	}

	want := []struct {
		method, path string
		body         map[string]any
	}{
		{"GET", "/api/v1/accounts/ACC1/templates", nil},
		{"POST", "/api/v1/accounts/ACC1/templates", map[string]any{
			"slug": "order-shipped", "name": "Order shipped", "from": "orders@acme.test",
			"variables": map[string]any{"orderRef": map[string]any{"sample": "A-1"}},
		}},
		{"GET", "/api/v1/accounts/ACC1/templates/order-shipped", nil},
		{"PATCH", "/api/v1/accounts/ACC1/templates/order-shipped", map[string]any{"replyTo": nil}},
		// No HTML means html:null, never an absent key or an empty string: a PUT
		// replaces the language's copy whole.
		{"PUT", "/api/v1/accounts/ACC1/templates/order-shipped/variants/de", map[string]any{
			"subject": "Versandt", "text": "Hallo", "html": nil,
		}},
		{"DELETE", "/api/v1/accounts/ACC1/templates/order-shipped/variants/de", nil},
		{"DELETE", "/api/v1/accounts/ACC1/templates/order-shipped", nil},
	}
	if len(*calls) != len(want) {
		t.Fatalf("calls = %d, want %d: %+v", len(*calls), len(want), *calls)
	}
	for i, w := range want {
		got := (*calls)[i]
		if got.method != w.method || got.path != w.path {
			t.Errorf("call %d = %s %s, want %s %s", i, got.method, got.path, w.method, w.path)
		}
		if !jsonEqual(got.body, w.body) {
			t.Errorf("call %d body = %v, want %v", i, got.body, w.body)
		}
	}
}

func TestTemplatesClientEscapesTheSlug(t *testing.T) {
	srv, calls := templateCore(t, func(*http.Request) (int, string) { return 404, `{"error":"not_found"}` })
	_, _ = testClient(t, srv.URL).GetTemplate(context.Background(), "ACC1", "a/b")
	if got := (*calls)[0].rawPath; got != "/api/v1/accounts/ACC1/templates/a%2Fb" {
		t.Fatalf("path = %q; a slug must stay ONE segment", got)
	}
}

// The render: values, the recipient built-ins, the preview switch and a draft,
// each sent only when given.
func TestTemplatesClientRender(t *testing.T) {
	srv, calls := templateCore(t, func(*http.Request) (int, string) {
		return 200, `{"slug":"order-shipped","langUsed":"de","templateVersion":3,"subject":"Versandt A-1","text":"Hallo",` +
			`"html":null,"source":"draft","variables":[{"name":"orderRef","description":null,"sample":"A-1","bodies":["subject"],"filledFromSample":true}]}`
	})
	c := testClient(t, srv.URL)
	out, err := c.RenderTemplate(context.Background(), "ACC1", "order-shipped", TemplateRenderInput{
		Lang:      "de",
		Vars:      map[string]any{"name": "Ann"},
		Recipient: &RenderRecipient{Email: "buyer@remote.test"},
		Preview:   true,
		Draft:     &VariantCopy{Subject: "Versandt {{orderRef}}", Text: "Hallo"},
	})
	if err != nil || out.Source != "draft" || out.Subject != "Versandt A-1" || len(out.Variables) != 1 || !out.Variables[0].FilledFromSample {
		t.Fatalf("RenderTemplate = %+v, %v", out, err)
	}
	got := (*calls)[0]
	if got.method != "POST" || got.path != "/api/v1/accounts/ACC1/templates/order-shipped/render" {
		t.Fatalf("call = %s %s", got.method, got.path)
	}
	want := map[string]any{
		"lang":      "de",
		"vars":      map[string]any{"name": "Ann"},
		"recipient": map[string]any{"email": "buyer@remote.test"},
		"preview":   true,
		"draft":     map[string]any{"subject": "Versandt {{orderRef}}", "text": "Hallo", "html": nil},
	}
	if !jsonEqual(got.body, want) {
		t.Fatalf("body = %v, want %v", got.body, want)
	}

	// A bare render sends an empty object: no lang, no preview, no draft.
	if _, err := c.RenderTemplate(context.Background(), "ACC1", "order-shipped", TemplateRenderInput{}); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual((*calls)[1].body, map[string]any{}) {
		t.Fatalf("bare render body = %v, want {}", (*calls)[1].body)
	}
}

// The send carries the batch's delivery id as the header core converges on,
// and a 207 is an ANSWER with rows, not an error: the rows say who to retry.
func TestTemplatesClientSend(t *testing.T) {
	srv, calls := templateCore(t, func(*http.Request) (int, string) {
		return 207, `{"slug":"order-shipped","langUsed":"en","templateVersion":3,"deliveryId":"batch-1","results":[` +
			`{"address":"a@remote.test","status":"queued","deliveryId":"batch-1.r1"},` +
			`{"address":"b@remote.test","status":"failed","error":"rate_limited","retryAfterSeconds":60,"deliveryId":"batch-1.r2"}]}`
	})
	save := false
	out, err := testClient(t, srv.URL).SendTemplate(context.Background(), "ACC1", "order-shipped", TemplateSendInput{
		Lang: "en",
		Vars: map[string]any{"orderRef": "A-1"},
		To: []TemplateRecipient{
			{Email: "a@remote.test"},
			{Email: "b@remote.test", Name: "Bea", Vars: map[string]any{"orderRef": "B-2"}},
		},
		ReplyTo: "help@acme.test",
		Headers: map[string]string{"X-Campaign": "autumn"},
		Save:    &save,
	}, "batch-1")
	if err != nil {
		t.Fatalf("a 207 must decode as an answer: %v", err)
	}
	if out.DeliveryID != "batch-1" || len(out.Results) != 2 || out.Results[1].Status != "failed" ||
		out.Results[1].RetryAfterSeconds == nil || *out.Results[1].RetryAfterSeconds != 60 {
		t.Fatalf("SendTemplate = %+v", out)
	}
	got := (*calls)[0]
	if got.path != "/api/v1/accounts/ACC1/templates/order-shipped/send" || got.header.Get("X-Delivery-Id") != "batch-1" {
		t.Fatalf("call = %s, X-Delivery-Id %q", got.path, got.header.Get("X-Delivery-Id"))
	}
	want := map[string]any{
		"lang": "en",
		"vars": map[string]any{"orderRef": "A-1"},
		"to": []any{
			map[string]any{"email": "a@remote.test"},
			map[string]any{"email": "b@remote.test", "name": "Bea", "vars": map[string]any{"orderRef": "B-2"}},
		},
		"replyTo": "help@acme.test",
		"headers": map[string]any{"X-Campaign": "autumn"},
		"save":    false,
	}
	if !jsonEqual(got.body, want) {
		t.Fatalf("body = %v, want %v", got.body, want)
	}
}

// Operator mail slots (core §VII): system routes, the same variant body, a
// draft on render and test, and no `vars` on a test send (core refuses them).
func TestMailSlotsClient(t *testing.T) {
	srv, calls := templateCore(t, func(r *http.Request) (int, string) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/system/mail-slots":
			return 200, `{"slots":[{"slug":"oe_reset","title":"Reset code","channel":"email_sending","constraints":["no_urls"],` +
				`"variables":[{"name":"code","required":true,"description":"The code","sample":"123456"}],` +
				`"defaultLanguages":["en"],"overriddenLanguages":["de"]}]}`
		case r.Method == http.MethodGet:
			return 200, `{"slug":"oe_reset","title":"Reset code","channel":"email_sending","constraints":["no_urls"],"variables":[],` +
				`"defaultLanguages":["en"],"defaults":[{"lang":"en","subject":"Code","text":"{{code}}"}],` +
				`"overrides":[{"lang":"de","subject":"Code","text":"{{code}}","html":null,"updatedAt":5}]}`
		case r.Method == http.MethodPut:
			return 200, `{"slug":"oe_reset","lang":"de","stored":true}`
		case r.Method == http.MethodDelete:
			return 200, `{"slug":"oe_reset","lang":"de","removed":true}`
		case r.URL.Path == "/api/v1/system/mail-slots/oe_reset/test":
			return 200, `{"slug":"oe_reset","langUsed":"de","sent":true}`
		default:
			return 200, `{"slug":"oe_reset","langUsed":"de","source":"override","subject":"Code","text":"123456","html":null,"variables":[]}`
		}
	})
	c := testClient(t, srv.URL)
	ctx := context.Background()

	slots, err := c.ListMailSlots(ctx)
	if err != nil || len(slots) != 1 || slots[0].OverriddenLanguages[0] != "de" || slots[0].Variables[0].Name != "code" {
		t.Fatalf("ListMailSlots = %+v, %v", slots, err)
	}
	slot, err := c.GetMailSlot(ctx, "oe_reset")
	if err != nil || len(slot.Defaults) != 1 || len(slot.Overrides) != 1 || slot.Overrides[0].UpdatedAt != 5 {
		t.Fatalf("GetMailSlot = %+v, %v", slot, err)
	}
	if _, err := c.PutMailSlotVariant(ctx, "oe_reset", "de", VariantCopy{Subject: "Code", Text: "{{code}}"}); err != nil {
		t.Fatal(err)
	}
	if out, err := c.DeleteMailSlotVariant(ctx, "oe_reset", "de"); err != nil || !out.Removed {
		t.Fatalf("DeleteMailSlotVariant = %+v, %v", out, err)
	}
	rendered, err := c.RenderMailSlot(ctx, "oe_reset", SlotRenderInput{Lang: "de", Draft: &VariantCopy{Subject: "C", Text: "{{code}}"}})
	if err != nil || rendered.Source != "override" {
		t.Fatalf("RenderMailSlot = %+v, %v", rendered, err)
	}
	sent, err := c.TestMailSlot(ctx, "oe_reset", SlotTestInput{To: "ops@acme.test", Lang: "de"})
	if err != nil || !sent.Sent {
		t.Fatalf("TestMailSlot = %+v, %v", sent, err)
	}

	want := []struct {
		method, path string
		body         map[string]any
	}{
		{"GET", "/api/v1/system/mail-slots", nil},
		{"GET", "/api/v1/system/mail-slots/oe_reset", nil},
		{"PUT", "/api/v1/system/mail-slots/oe_reset/variants/de", map[string]any{"subject": "Code", "text": "{{code}}", "html": nil}},
		{"DELETE", "/api/v1/system/mail-slots/oe_reset/variants/de", nil},
		{"POST", "/api/v1/system/mail-slots/oe_reset/render", map[string]any{
			"lang": "de", "draft": map[string]any{"subject": "C", "text": "{{code}}", "html": nil},
		}},
		{"POST", "/api/v1/system/mail-slots/oe_reset/test", map[string]any{"to": "ops@acme.test", "lang": "de"}},
	}
	for i, w := range want {
		got := (*calls)[i]
		if got.method != w.method || got.path != w.path || !jsonEqual(got.body, w.body) {
			t.Errorf("call %d = %s %s %v, want %s %s %v", i, got.method, got.path, got.body, w.method, w.path, w.body)
		}
	}
}

// jsonEqual compares two decoded JSON values by their canonical encoding.
func jsonEqual(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}
