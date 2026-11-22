// Package xsdlite validates XML documents against W3C XML Schema 1.0.
//
// It uses only the Go standard library, and reuses encoding/xml and regexp
// rather than carrying its own parser and regular-expression engine, so it
// adds little to a binary. Compile rejects a schema that needs a part of XML
// Schema the package does not implement; it never validates against less than
// the schema says. The README lists what is covered.
//
// Compile a schema once, then validate any number of documents with it, from
// any number of goroutines:
//
//	schema, err := xsdlite.Compile(files, "root.xsd")
//	result, err := schema.Validate(reader, xsdlite.Options{})
package xsdlite

import (
	"fmt"
	"io"
	"io/fs"
)

// Code classifies a violation.
type Code string

const (
	// CodeRoot: the document's root element is not declared by the schema.
	CodeRoot Code = "root"
	// CodeElement: an element that is not allowed where it appears.
	CodeElement Code = "element"
	// CodeContent: required content is missing, or text appears where only
	// elements are allowed.
	CodeContent Code = "content"
	// CodeAttribute: a missing, undeclared or wrongly valued attribute.
	CodeAttribute Code = "attribute"
	// CodeValue: element text that breaks its simple type.
	CodeValue Code = "value"
	// CodeType: an xsi:type or xsi:nil that the schema does not permit, or an
	// abstract element or type used directly.
	CodeType Code = "type"
	// CodeIdentity: a broken unique, key or keyref constraint, or a duplicate
	// or dangling ID.
	CodeIdentity Code = "identity"
)

// Violation is one place where a document breaks the schema.
type Violation struct {
	Code    Code
	Message string
	// Path is the element path without positions, for example /order/item.
	// For an element that is not allowed it is the path of the parent.
	Path string
	// Line and Column locate the start of the element concerned.
	Line, Column int
}

func (v Violation) String() string {
	return fmt.Sprintf("%d:%d %s: %s (%s)", v.Line, v.Column, v.Path, v.Message, v.Code)
}

// Result is the outcome of validating a well-formed document.
type Result struct {
	// Violations are in document order. Keyref violations are reported where
	// the element that declares the constraint ends.
	Violations []Violation
	// Truncated is true when Options.MaxViolations stopped the assessment.
	Truncated bool
}

// Valid reports whether the document conforms to the schema.
func (r Result) Valid() bool { return len(r.Violations) == 0 && !r.Truncated }

// Options controls one validation.
type Options struct {
	// MaxViolations stops the assessment after this many violations. Zero
	// means 1000.
	MaxViolations int
}

// SyntaxError reports a document that is not well-formed XML. It is returned
// as the error of Validate; such a document has no validity result.
type SyntaxError struct {
	Line int
	Msg  string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// SchemaError reports a schema that cannot be compiled.
type SchemaError struct {
	// File and Line locate the schema component, when known.
	File string
	Line int
	Msg  string
	// Unsupported is true when the schema is sound but uses a part of XML
	// Schema that this package does not implement.
	Unsupported bool
}

func (e *SchemaError) Error() string {
	if e.File == "" {
		return e.Msg
	}
	return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Msg)
}

// Schema is a compiled schema. It is immutable and safe for concurrent use.
type Schema struct {
	elements    map[xname]*elementDecl
	types       map[xname]*typeDef
	attributes  map[xname]*attrUse
	constraints map[xname]*constraint
	anyType     *typeDef
	target      string
	hasIDs      bool
}

// Compile reads the schema document named root from fsys, follows its
// xs:include and xs:import elements relative to it, and compiles the result.
// More roots may be given for schemas of other namespaces that are imported
// without a schemaLocation.
func Compile(fsys fs.FS, root string, more ...string) (*Schema, error) {
	c := newCompiler(fsys)
	for _, name := range append([]string{root}, more...) {
		if err := c.load(name, nil); err != nil {
			return nil, err
		}
	}
	return c.compile()
}

// Validate reads one XML document and assesses it against the schema. The
// error is non-nil only when the document cannot be read or is not well-formed.
func (s *Schema) Validate(r io.Reader, opts Options) (Result, error) {
	v := newValidator(s, opts)
	return v.run(r)
}

// xname is an XML name with its namespace.
type xname struct {
	Space, Local string
}

func (q xname) String() string {
	if q.Space == "" {
		return q.Local
	}
	return "{" + q.Space + "}" + q.Local
}

const (
	xsdNS = "http://www.w3.org/2001/XMLSchema"
	xsiNS = "http://www.w3.org/2001/XMLSchema-instance"
	xmlNS = "http://www.w3.org/XML/1998/namespace"
)
