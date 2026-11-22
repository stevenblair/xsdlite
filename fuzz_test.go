package xsdlite

import (
	"strings"
	"testing"
)

// FuzzValidate feeds arbitrary documents to the validator. Whatever the
// input, validation must return rather than panic, and a document reported
// as valid must stay valid when validated again.
func FuzzValidate(f *testing.F) {
	schema := featureSchema(f)
	doc := featureDocument(f)
	f.Add(doc)
	f.Add(`<catalog xmlns="urn:xsdlite:test" version="1.0"><title>t</title><category code="AB">c</category></catalog>`)
	f.Add(`<x xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="a:b" xsi:nil="true"/>`)
	f.Fuzz(func(t *testing.T, document string) {
		first, err := schema.Validate(strings.NewReader(document), Options{MaxViolations: 50})
		if err != nil {
			return
		}
		second, err := schema.Validate(strings.NewReader(document), Options{MaxViolations: 50})
		if err != nil || len(first.Violations) != len(second.Violations) {
			t.Fatalf("two runs disagree: %d and %d violations, err %v", len(first.Violations), len(second.Violations), err)
		}
	})
}
