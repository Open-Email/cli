package coreapi

// DmarcTotals is the one DMARC wire struct the table-driven harness cannot
// reach: core inlines the totals object under DomainDmarc.totals instead of
// naming it as a component, and contractPairings indexes components by name.
// The DomainDmarc pairing only proves `totals` is *an object* — every field
// inside it would drift unnoticed. This walks into the inline schema and runs
// the same comparison against it.

import (
	"reflect"
	"testing"
)

func TestDmarcTotalsMatchesInlineSchema(t *testing.T) {
	spec := loadSnapshot(t)
	schemas, ok := spec["components"].(map[string]any)["schemas"].(map[string]any)
	if !ok {
		t.Fatal("snapshot has no components.schemas")
	}
	comp, ok := schemas["DomainDmarc"].(map[string]any)
	if !ok {
		t.Fatal("component \"DomainDmarc\" not found in snapshot")
	}
	props := resolveComponent(t, schemas, comp)
	totals, ok := props["totals"].(map[string]any)
	if !ok {
		t.Fatal("DomainDmarc.totals is not an inline object schema (did core extract it to a component? pair it in contractPairings instead)")
	}
	tprops := resolveComponent(t, schemas, totals)
	if len(tprops) == 0 {
		t.Fatal("DomainDmarc.totals has no properties — the walk found the wrong node")
	}
	goFields := flattenGoFields(reflect.TypeOf(DmarcTotals{}))
	for _, issue := range compare(pairing{comp: "DomainDmarc.totals"}, schemas, tprops, goFields) {
		t.Error(issue)
	}
}
