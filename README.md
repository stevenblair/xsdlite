# xsdlite

A small XML Schema 1.0 validator in pure Go.

xsdlite checks an XML document against an XSD and reports each violation with
its element path, line and column. It uses only the Go standard library, with
no cgo, so it builds for every Go target, including WebAssembly and mobile.

It is built to stay small: it reuses `encoding/xml` and `regexp` instead of
carrying its own parser and regular-expression engine. In a program that
already links those, it adds about 0.7 MB to a WebAssembly build, or 0.2 MB
compressed.

## Use

```
go get github.com/stevenblair/xsdlite
```

```go
schema, err := xsdlite.Compile(os.DirFS("schemas"), "order.xsd")
if err != nil {
    return err // the schema is invalid, or needs something xsdlite lacks
}
result, err := schema.Validate(file, xsdlite.Options{})
if err != nil {
    return err // the document is not well-formed XML
}
for _, v := range result.Violations {
    fmt.Println(v) // 14:3 /order/item: missing required attribute "sku" (attribute)
}
```

- `Compile` takes any `fs.FS`, so schemas can be embedded with `go:embed`.
- `xs:include` and `xs:import` are resolved inside that file system. Nothing is
  fetched from the network.
- A compiled `Schema` is immutable and safe to use from many goroutines.
- Validation is streaming: the document is never held in memory.
- Requires Go 1.23 or later.

There is also a command:

```
go install github.com/stevenblair/xsdlite/cmd/xsdlite@latest
xsdlite schema.xsd document.xml
```

## Violations

Each `Violation` has a `Code`, a `Message`, a `Path` and a position.

| Code | Meaning |
| --- | --- |
| `root` | The root element is not declared. |
| `element` | An element is not allowed where it appears. |
| `content` | Required content is missing, or there is text where only elements are allowed. |
| `attribute` | An attribute is missing, undeclared or has an invalid value. |
| `value` | Element text does not fit its simple type. |
| `type` | An `xsi:type` or `xsi:nil` the schema does not permit, or an abstract type used directly. |
| `identity` | A `unique`, `key` or `keyref` constraint is broken, or an ID is duplicated or dangling. |

One mistake is reported once. A misplaced element is still validated against
its own declaration, so its contents are checked and a missing sibling does
not cause a cascade of errors. Identity messages name the value and the
constraint:

```
duplicate value "A7" for key "partKey"; it is first used on line 12
```

## Supported

- `include` (including chameleon includes) and `import`.
- Elements: global and local, `ref`, occurrence bounds, `default`, `fixed`,
  `nillable`, `abstract` and substitution groups.
- Complex types: `sequence`, `choice`, `all`, named groups, mixed, empty and
  simple content, extension and restriction, `xsi:type` and `xsi:nil`.
- Attributes: `use`, `default`, `fixed`, attribute groups and the built-in
  `xml:` attributes.
- Wildcards: `any` and `anyAttribute`, with every namespace form and
  `strict`, `lax` and `skip` processing.
- Simple types: every built-in type, restriction, list and union, and all
  twelve facets. Values are compared in their value space, so `1.50` equals
  `+01.5`.
- Patterns: the full XML Schema syntax, including `\i`, `\c`, Unicode
  categories, `\p{IsBlock}` and character-class subtraction.
- Identity constraints: `unique`, `key`, `keyref`, and `ID`/`IDREF`/`IDREFS`.
- Ambiguous content models (Unique Particle Attribution violations) are
  rejected when the schema is compiled.

## Not supported

`Compile` rejects a schema that needs any of these, with a `SchemaError` whose
`Unsupported` field is true. xsdlite never silently validates against less
than the schema says.

- `xs:redefine`.
- XML Schema 1.1.
- Extending a type whose content is an `xs:all` group.
- Bounds such as `minInclusive` on `duration` and the `gYear` family.
- Patterns with a repeat count above 1000.
- XPath in identity constraints beyond the subset the specification defines.

These are accepted but not enforced:

- `block` and `final`.
- That a type derived by restriction is a true restriction of its base.
  Documents are checked against the derived type as written.
- Time zones when comparing dates and times: a value without a zone is treated
  as UTC.

Documents may be UTF-8, US-ASCII or ISO-8859-1. DTDs are not applied: a
document that uses an entity declared in a DTD is reported as not well-formed.

## Tests

```
go test ./...
```

`testdata/features.xsd` uses every supported feature at least once. The tests
make 57 single edits to a valid document and check that each produces the
expected violation.

`difftest` is a separate module, so that xsdlite itself has no dependencies.
It makes random edits to valid documents and checks that xsdlite and an
independent validator, [jacoelho/xsd](https://github.com/jacoelho/xsd), agree
on which documents are valid:

```
cd difftest
go run . -n 20000 ../testdata/features.xsd ../testdata/features.xml
```

## Licence

MIT. See [LICENSE](LICENSE).
