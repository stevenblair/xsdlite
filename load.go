package xsdlite

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// nsScope is the chain of namespace declarations in scope at a schema element.
type nsScope struct {
	parent      *nsScope
	prefix, uri string
}

func (s *nsScope) resolvePrefix(prefix string) (string, bool) {
	for ; s != nil; s = s.parent {
		if s.prefix == prefix {
			return s.uri, true
		}
	}
	if prefix == "xml" {
		return xmlNS, true
	}
	return "", false
}

// resolver turns a namespace prefix into its URI where a value is read.
type resolver interface {
	resolvePrefix(prefix string) (string, bool)
}

// node is one element of a schema document. Annotations are dropped.
type node struct {
	local    string
	attrs    map[string]string
	children []*node
	ns       *nsScope
	doc      *document
	line     int
}

func (n *node) first(names ...string) *node {
	for _, child := range n.children {
		for _, name := range names {
			if child.local == name {
				return child
			}
		}
	}
	return nil
}

func (n *node) flag(name string) bool {
	v := strings.TrimSpace(n.attrs[name])
	return v == "true" || v == "1"
}

type document struct {
	name                         string
	target                       string
	chameleon                    bool
	elemQualified, attrQualified bool
	root                         *node
}

func parseSchemaDocument(name string, data []byte) (*document, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	doc := &document{name: name}
	var stack []*node
	skip := 0
	for {
		line, _ := decoder.InputPos()
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, &SchemaError{File: name, Line: line, Msg: "the schema is not well-formed XML: " + err.Error()}
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 {
				skip++
				continue
			}
			if t.Name.Space != xsdNS || t.Name.Local == "annotation" {
				if len(stack) == 0 {
					return nil, &SchemaError{File: name, Line: line, Msg: "the root element is not xs:schema"}
				}
				skip = 1
				continue
			}
			n := &node{local: t.Name.Local, doc: doc, line: line, attrs: map[string]string{}}
			var scope *nsScope
			if len(stack) > 0 {
				scope = stack[len(stack)-1].ns
			}
			for _, a := range t.Attr {
				switch {
				case a.Name.Space == "xmlns":
					scope = &nsScope{parent: scope, prefix: a.Name.Local, uri: a.Value}
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					scope = &nsScope{parent: scope, uri: a.Value}
				case a.Name.Space == "":
					n.attrs[a.Name.Local] = a.Value
				}
			}
			n.ns = scope
			if len(stack) == 0 {
				if n.local != "schema" {
					return nil, &SchemaError{File: name, Line: line, Msg: "the root element is not xs:schema"}
				}
				doc.root = n
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			stack = stack[:len(stack)-1]
		}
	}
	if doc.root == nil {
		return nil, &SchemaError{File: name, Msg: "the schema document is empty"}
	}
	doc.target = doc.root.attrs["targetNamespace"]
	doc.elemQualified = doc.root.attrs["elementFormDefault"] == "qualified"
	doc.attrQualified = doc.root.attrs["attributeFormDefault"] == "qualified"
	return doc, nil
}

// compiler turns schema documents into a Schema.
type compiler struct {
	fsys fs.FS
	docs map[string]*document
	s    *Schema

	rawTypes, rawElements, rawAttrs, rawAttrGroups, rawGroups map[xname]*node

	anyType, anySimple *typeDef
	localElems         map[*node]*elementDecl
	pending            []func() error
	complexTypes       []*typeDef
	allTypes           []*typeDef
	attrUses           []*attrUse
	subst              map[xname][]xname
	allConstraints     []*constraint
	groupDepth         int
}

func newCompiler(fsys fs.FS) *compiler {
	return &compiler{
		fsys: fsys,
		docs: map[string]*document{},
		s: &Schema{
			elements:    map[xname]*elementDecl{},
			types:       map[xname]*typeDef{},
			attributes:  map[xname]*attrUse{},
			constraints: map[xname]*constraint{},
		},
		rawTypes: map[xname]*node{}, rawElements: map[xname]*node{}, rawAttrs: map[xname]*node{},
		rawAttrGroups: map[xname]*node{}, rawGroups: map[xname]*node{},
		localElems: map[*node]*elementDecl{},
		subst:      map[xname][]xname{},
	}
}

func (c *compiler) errf(n *node, format string, args ...any) error {
	e := &SchemaError{Msg: fmt.Sprintf(format, args...)}
	if n != nil {
		e.File, e.Line = n.doc.name, n.line
	}
	return e
}

func (c *compiler) unsupported(n *node, format string, args ...any) error {
	e := c.errf(n, format, args...).(*SchemaError)
	e.Unsupported = true
	e.Msg += " (not supported by xsdlite)"
	return e
}

// load reads one schema document and everything it includes or imports.
// from is the including element, or nil for a root.
func (c *compiler) load(location string, from *node) error {
	name := path.Clean(location)
	if from != nil {
		name = path.Clean(path.Join(path.Dir(from.doc.name), location))
	}
	if _, ok := c.docs[name]; ok {
		return nil
	}
	data, err := fs.ReadFile(c.fsys, name)
	if err != nil {
		if from != nil && from.local == "import" && errors.Is(err, fs.ErrNotExist) {
			// An import's location is a hint; its namespace may be supplied
			// as another root. Missing components are reported when used.
			return nil
		}
		if from != nil {
			return c.errf(from, "cannot read %s: %v", name, err)
		}
		return &SchemaError{File: name, Msg: "cannot read the schema: " + err.Error()}
	}
	doc, err := parseSchemaDocument(name, data)
	if err != nil {
		return err
	}
	if from != nil && from.local == "include" {
		switch {
		case doc.target == "" && from.doc.target != "":
			doc.target, doc.chameleon = from.doc.target, true
		case doc.target != from.doc.target:
			return c.errf(from, "%s has target namespace %q, but an included schema must have %q", name, doc.target, from.doc.target)
		}
	}
	c.docs[name] = doc
	if c.s.target == "" {
		c.s.target = doc.target
	}
	for _, n := range doc.root.children {
		var into map[xname]*node
		switch n.local {
		case "include", "import":
			if loc := n.attrs["schemaLocation"]; loc != "" {
				if err := c.load(loc, n); err != nil {
					return err
				}
			} else if n.local == "include" {
				return c.errf(n, "xs:include has no schemaLocation")
			}
			continue
		case "redefine":
			return c.unsupported(n, "xs:redefine")
		case "notation":
			continue
		case "element":
			into = c.rawElements
		case "complexType", "simpleType":
			into = c.rawTypes
		case "attribute":
			into = c.rawAttrs
		case "attributeGroup":
			into = c.rawAttrGroups
		case "group":
			into = c.rawGroups
		default:
			return c.errf(n, "xs:%s is not allowed at the top of a schema", n.local)
		}
		name := n.attrs["name"]
		if name == "" {
			return c.errf(n, "a top-level xs:%s needs a name", n.local)
		}
		q := xname{doc.target, name}
		if into[q] != nil {
			return c.errf(n, "%s %s is defined twice", n.local, q)
		}
		into[q] = n
	}
	return nil
}

// qname resolves a QName written in a schema attribute.
func (c *compiler) qname(n *node, value string) (xname, error) {
	value = strings.TrimSpace(value)
	prefix, local, found := strings.Cut(value, ":")
	if !found {
		prefix, local = "", value
	}
	if local == "" {
		return xname{}, c.errf(n, "%q is not a qualified name", value)
	}
	uri, ok := n.ns.resolvePrefix(prefix)
	if !ok {
		if prefix != "" {
			return xname{}, c.errf(n, "the namespace prefix %q is not declared", prefix)
		}
		if n.doc.chameleon {
			uri = n.doc.target
		}
	}
	return xname{uri, local}, nil
}

func sortedNames[V any](m map[xname]V) []xname {
	names := make([]xname, 0, len(m))
	for q := range m {
		names = append(names, q)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i].Space != names[j].Space {
			return names[i].Space < names[j].Space
		}
		return names[i].Local < names[j].Local
	})
	return names
}
