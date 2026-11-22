package xsdlite

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// validator assesses one document as it is read. It keeps one frame per open
// element and never holds the document in memory.
type validator struct {
	s       *Schema
	max     int
	result  Result
	stopped bool

	frames    []frame
	ns        []nsBinding
	attrs     []attrValue
	scopes    []*scope
	targets   []*target
	tuple     []byte
	rootSeen  bool
	line, col int

	paths     []pathNode
	pathIndex map[pathKey]int32

	ids    map[string]struct{}
	idrefs []idref
}

// maxDepth bounds element nesting, so a hostile document cannot grow the
// frame stack without limit.
const maxDepth = 5000

type nsBinding struct{ prefix, uri string }

// attrValue is an attribute of the element being opened. kind is zero when
// the value is not valid; such an attribute is present but has no value.
type attrValue struct {
	name xname
	key  string
	kind prim
}

type pathNode struct {
	parent int32
	name   string
}

type pathKey struct {
	parent int32
	name   xname
}

type idref struct {
	value     string
	path      int32
	line, col int
}

type frame struct {
	name      xname
	decl      *elementDecl
	typ       *typeDef
	path      int32
	line, col int
	// skip marks a subtree that is not assessed.
	skip bool
	// Content model position.
	state  int32
	seen   []bool
	broken bool

	nilled, hasChild, textReported bool
	collect                        bool
	text                           []byte

	nsMark, scopeMark int
	// reported is the number of violations when the element was opened.
	reported   int
	selfFields []fieldRef
	inherited  map[*constraint]map[string]int32
}

func newValidator(s *Schema, opts Options) *validator {
	v := &validator{s: s, max: opts.MaxViolations, pathIndex: map[pathKey]int32{}}
	if v.max <= 0 {
		v.max = 1000
	}
	return v
}

func (v *validator) resolvePrefix(prefix string) (string, bool) {
	for i := len(v.ns) - 1; i >= 0; i-- {
		if v.ns[i].prefix == prefix {
			return v.ns[i].uri, true
		}
	}
	if prefix == "xml" {
		return xmlNS, true
	}
	return "", prefix == ""
}

func (v *validator) run(r io.Reader) (Result, error) {
	decoder := xml.NewDecoder(r)
	decoder.CharsetReader = charsetReader
	for {
		v.line, v.col = decoder.InputPos()
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			line := v.line
			var syntax *xml.SyntaxError
			if errors.As(err, &syntax) {
				return Result{}, &SyntaxError{Line: syntax.Line, Msg: syntax.Msg}
			}
			return Result{}, &SyntaxError{Line: line, Msg: err.Error()}
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(v.frames) == 0 && v.rootSeen {
				return Result{}, &SyntaxError{Line: v.line, Msg: "the document has more than one root element"}
			}
			if len(v.frames) >= maxDepth {
				return Result{}, &SyntaxError{Line: v.line, Msg: "elements are nested more than " + strconv.Itoa(maxDepth) + " deep"}
			}
			v.rootSeen = true
			v.start(t)
		case xml.EndElement:
			v.end()
		case xml.CharData:
			if len(v.frames) == 0 {
				if !allSpace(t) {
					return Result{}, &SyntaxError{Line: v.line, Msg: "text outside the root element"}
				}
				continue
			}
			v.chars(t)
		}
	}
	if !v.rootSeen {
		return Result{}, &SyntaxError{Line: v.line, Msg: "the document has no root element"}
	}
	for _, ref := range v.idrefs {
		if _, ok := v.ids[ref.value]; !ok {
			v.report(CodeIdentity, ref.path, ref.line, ref.col, "the IDREF %q does not match any ID in the document", shorten(ref.value))
		}
	}
	return v.result, nil
}

func charsetReader(label string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return input, nil
	case "iso-8859-1", "latin1":
		return &latin1Reader{in: input}, nil
	}
	return nil, fmt.Errorf("the text encoding %q is not supported", label)
}

// latin1Reader converts ISO-8859-1 to UTF-8.
type latin1Reader struct {
	in      io.Reader
	pending []byte
	buf     [512]byte
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	for len(l.pending) == 0 {
		n, err := l.in.Read(l.buf[:])
		for _, b := range l.buf[:n] {
			l.pending = utf8.AppendRune(l.pending, rune(b))
		}
		if err != nil && len(l.pending) == 0 {
			return 0, err
		}
	}
	n := copy(p, l.pending)
	l.pending = l.pending[n:]
	return n, nil
}

func allSpace(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return false
		}
	}
	return true
}

func shorten(s string) string {
	if utf8.RuneCountInString(s) <= 60 {
		return s
	}
	return string([]rune(s)[:57]) + "..."
}

// display names an element or attribute: the local name, with the namespace
// only when it is not the schema's own.
func (v *validator) display(q xname) string {
	if q.Space == "" || q.Space == v.s.target {
		return q.Local
	}
	return q.String()
}

func (v *validator) report(code Code, path int32, line, col int, format string, args ...any) {
	if v.stopped {
		return
	}
	v.result.Violations = append(v.result.Violations, Violation{Code: code, Message: fmt.Sprintf(format, args...), Path: v.pathText(path), Line: line, Column: col})
	if len(v.result.Violations) >= v.max {
		v.stopped, v.result.Truncated = true, true
	}
}

func (v *validator) pathID(parent int32, name xname) int32 {
	key := pathKey{parent, name}
	if id, ok := v.pathIndex[key]; ok {
		return id
	}
	v.paths = append(v.paths, pathNode{parent: parent, name: v.display(name)})
	id := int32(len(v.paths))
	v.pathIndex[key] = id
	return id
}

func (v *validator) pathText(id int32) string {
	var parts []string
	for ; id > 0; id = v.paths[id-1].parent {
		parts = append(parts, v.paths[id-1].name)
	}
	var b strings.Builder
	for i := len(parts) - 1; i >= 0; i-- {
		b.WriteByte('/')
		b.WriteString(parts[i])
	}
	return b.String()
}

func (v *validator) start(t xml.StartElement) {
	mark := len(v.ns)
	for _, a := range t.Attr {
		switch {
		case a.Name.Space == "xmlns":
			v.ns = append(v.ns, nsBinding{a.Name.Local, a.Value})
		case a.Name.Space == "" && a.Name.Local == "xmlns":
			v.ns = append(v.ns, nsBinding{"", a.Value})
		}
	}
	f := frame{name: xname(t.Name), line: v.line, col: v.col, nsMark: mark, scopeMark: len(v.scopes), reported: len(v.result.Violations)}
	depth := len(v.frames)
	if depth == 0 {
		f.path = v.pathID(0, f.name)
		if f.decl = v.s.elements[f.name]; f.decl == nil {
			v.report(CodeRoot, f.path, f.line, f.col, "the root element %q is not declared by the schema", v.display(f.name))
			f.skip = true
		}
	} else {
		parent := &v.frames[depth-1]
		f.path = v.pathID(parent.path, f.name)
		if parent.skip || v.stopped {
			f.skip = true
		} else {
			v.child(parent, &f)
		}
		parent.hasChild = true
	}
	// Reuse the text buffer of an earlier element at this depth.
	if depth < cap(v.frames) {
		f.text = v.frames[:depth+1][depth].text[:0]
	}
	v.frames = append(v.frames, f)
	if !f.skip {
		v.open(depth, t)
	}
}

// child places a child element in its parent's content model and decides
// what, if anything, it is validated against.
func (v *validator) child(parent, f *frame) {
	if parent.nilled {
		if !parent.broken {
			parent.broken = true
			v.report(CodeElement, parent.path, f.line, f.col, "unexpected child element %q; an element with xsi:nil must be empty", v.display(f.name))
		}
		f.skip = true
		return
	}
	model := parent.typ.model
	if model == nil {
		if !parent.broken {
			parent.broken = true
			v.report(CodeElement, parent.path, f.line, f.col, "unexpected child element %q; no child elements are allowed here", v.display(f.name))
		}
		f.skip = true
		return
	}
	if model.all {
		if i, ok := model.allIndex[f.name]; ok {
			if parent.seen[i] {
				v.report(CodeElement, parent.path, f.line, f.col, "element %q may appear only once here", v.display(f.name))
			}
			parent.seen[i] = true
			f.decl = model.declared[f.name]
			return
		}
		v.unexpected(parent, f, v.expectedAll(parent))
		f.skip = true
		return
	}
	state := &model.states[parent.state]
	if tr, ok := state.next[f.name]; ok {
		parent.state, f.decl = tr.state, tr.decl
		return
	}
	for _, w := range state.wild {
		if !w.wild.allows(f.name.Space) {
			continue
		}
		parent.state = w.state
		switch decl := v.s.elements[f.name]; {
		case w.wild.process == processSkip:
			f.skip = true
		case decl != nil:
			f.decl = decl
		case w.wild.process == processStrict:
			v.report(CodeElement, parent.path, f.line, f.col, "element %q is not declared by the schema", v.display(f.name))
			f.skip = true
		default:
			f.typ = v.s.anyType
		}
		return
	}
	v.unexpected(parent, f, v.expected(state))
	// Validate a misplaced element against its declaration all the same, so
	// one mistake is reported once and what it contains is still checked.
	if decl := model.declared[f.name]; decl != nil {
		f.decl = decl
	} else {
		f.skip = true
	}
}

func (v *validator) unexpected(parent, f *frame, expected string) {
	if parent.broken {
		return
	}
	parent.broken = true
	v.report(CodeElement, parent.path, f.line, f.col, "unexpected child element %q; %s", v.display(f.name), expected)
}

const maxExpected = 6

func (v *validator) expectedNames(names []string) string {
	switch len(names) {
	case 0:
		return "no further child elements are allowed here"
	case 1:
		return "expected " + names[0]
	}
	more := ""
	if len(names) > maxExpected {
		more = " or " + strconv.Itoa(len(names)-maxExpected) + " more"
		names = names[:maxExpected]
	}
	return "expected one of " + strings.Join(names, ", ") + more
}

func (v *validator) expected(state *modelState) string {
	names := make([]string, 0, len(state.order))
	seen := map[string]bool{}
	for _, q := range state.order {
		if name := v.display(q); !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 && len(state.wild) > 0 {
		return "only elements from another namespace are allowed here"
	}
	return v.expectedNames(names)
}

func (v *validator) expectedAll(f *frame) string {
	var names []string
	model := f.typ.model
	for i, item := range model.allItems {
		if !f.seen[i] {
			names = append(names, v.display(item.decl.name))
		}
	}
	return v.expectedNames(names)
}

// open settles the type of the element just pushed and validates its attributes.
func (v *validator) open(depth int, t xml.StartElement) {
	f := &v.frames[depth]
	typ := f.typ
	if f.decl != nil {
		typ = f.decl.typ
		if f.decl.abstract {
			v.report(CodeType, f.path, f.line, f.col, "element %q is abstract and cannot be used directly", v.display(f.name))
		}
	}
	for _, a := range t.Attr {
		if a.Name.Space != xsiNS {
			continue
		}
		switch a.Name.Local {
		case "type":
			key, ok := parsePrimitive(pQName, strings.TrimSpace(a.Value), v)
			var td *typeDef
			if ok {
				space, local, _ := strings.Cut(strings.TrimPrefix(key, "{"), "}")
				td = v.s.types[xname{space, local}]
			}
			switch {
			case td == nil:
				v.report(CodeType, f.path, f.line, f.col, "xsi:type %q is not a type the schema defines", a.Value)
			case !td.derivesFrom(typ):
				v.report(CodeType, f.path, f.line, f.col, "xsi:type %q is not derived from %s, the declared type of %q", a.Value, typ.label(), v.display(f.name))
			default:
				typ = td
			}
		case "nil":
			switch strings.TrimSpace(a.Value) {
			case "true", "1":
				if f.decl == nil {
					// A laxly assessed element with no declaration.
				} else if !f.decl.nillable {
					v.report(CodeType, f.path, f.line, f.col, "xsi:nil is not allowed on element %q", v.display(f.name))
				} else {
					f.nilled = true
				}
			case "false", "0":
			default:
				v.report(CodeType, f.path, f.line, f.col, "xsi:nil has the value %q; it must be true or false", shorten(a.Value))
			}
		case "schemaLocation", "noNamespaceSchemaLocation":
		default:
			v.report(CodeAttribute, f.path, f.line, f.col, "attribute %q is not allowed", "xsi:"+a.Name.Local)
		}
	}
	if typ.abstract {
		v.report(CodeType, f.path, f.line, f.col, "%s is abstract; element %q needs an xsi:type that names a concrete type", typ.label(), v.display(f.name))
	}
	f.typ = typ
	if typ.model != nil && typ.model.all {
		f.seen = make([]bool, len(typ.model.allItems))
	}
	f.collect = typ.simple != nil || (f.decl != nil && f.decl.fixed != nil)

	v.attrs = v.attrs[:0]
	for _, a := range t.Attr {
		if a.Name.Space == "xmlns" || a.Name.Space == xsiNS || (a.Name.Space == "" && a.Name.Local == "xmlns") {
			continue
		}
		q := xname(a.Name)
		use := typ.attrs[q]
		if use == nil {
			w := typ.anyAttr
			if !typ.complex || w == nil || !w.allows(q.Space) {
				v.report(CodeAttribute, f.path, f.line, f.col, "attribute %q is not allowed", v.display(q))
				continue
			}
			if w.process == processSkip {
				continue
			}
			if use = v.s.attributes[q]; use == nil {
				if w.process == processStrict {
					v.report(CodeAttribute, f.path, f.line, f.col, "attribute %q is not declared by the schema", v.display(q))
				} else {
					// Not assessed, so it has no value for identity constraints.
					v.attrs = append(v.attrs, attrValue{name: q})
				}
				continue
			}
		}
		st := use.typ.simple
		key, kind, reason := st.validate(a.Value, v)
		if reason != "" {
			v.report(CodeAttribute, f.path, f.line, f.col, "attribute %q: value %q %s", v.display(q), shorten(a.Value), reason)
			v.attrs = append(v.attrs, attrValue{name: q})
			continue
		}
		if use.fixed != nil && key != use.key {
			v.report(CodeAttribute, f.path, f.line, f.col, "attribute %q has value %q; expected fixed value %q", v.display(q), shorten(a.Value), *use.fixed)
		}
		if v.s.hasIDs {
			v.noteID(st, key, f)
		}
		v.attrs = append(v.attrs, attrValue{q, key, kind})
	}
	for _, use := range typ.required {
		if !v.hasAttr(use.name) {
			v.report(CodeAttribute, f.path, f.line, f.col, "missing required attribute %q", v.display(use.name))
		}
	}
	for _, use := range typ.defaults {
		if !v.hasAttr(use.name) {
			v.attrs = append(v.attrs, attrValue{use.name, use.key, use.kind})
		}
	}
	if len(v.scopes) > 0 || len(v.targets) > 0 || (f.decl != nil && len(f.decl.constraints) > 0) {
		v.identityStart(depth)
	}
}

func (v *validator) hasAttr(name xname) bool {
	for i := range v.attrs {
		if v.attrs[i].name == name {
			return true
		}
	}
	return false
}

func (v *validator) noteID(st *simpleType, key string, f *frame) {
	switch st.idKind() {
	case 1:
		if v.ids == nil {
			v.ids = map[string]struct{}{}
		}
		if _, dup := v.ids[key]; dup {
			v.report(CodeIdentity, f.path, f.line, f.col, "the ID %q is used more than once", shorten(key))
		}
		v.ids[key] = struct{}{}
	case 2:
		for _, value := range strings.Fields(key) {
			v.idrefs = append(v.idrefs, idref{value, f.path, f.line, f.col})
		}
	}
}

func (v *validator) chars(data []byte) {
	f := &v.frames[len(v.frames)-1]
	if f.skip {
		return
	}
	if f.collect || len(f.selfFields) > 0 {
		f.text = append(f.text, data...)
	}
	if f.textReported || allSpace(data) {
		return
	}
	typ := f.typ
	switch {
	case f.nilled:
		f.textReported = true
		v.report(CodeContent, f.path, f.line, f.col, "an element with xsi:nil must be empty")
	case typ.complex && typ.simple == nil && !typ.mixed:
		f.textReported = true
		if typ.model == nil {
			v.report(CodeContent, f.path, f.line, f.col, "text is not allowed here; the element must be empty")
		} else {
			v.report(CodeContent, f.path, f.line, f.col, "text is not allowed here; the element may only contain child elements")
		}
	}
}

func (v *validator) end() {
	depth := len(v.frames) - 1
	f := &v.frames[depth]
	if !f.skip {
		typ := f.typ
		if model := typ.model; model != nil && !f.broken && !f.nilled {
			if model.all {
				missing := false
				for i, item := range model.allItems {
					missing = missing || (item.required && !f.seen[i])
				}
				if missing && (f.hasChild || !model.allOptional) {
					var names []string
					for i, item := range model.allItems {
						if item.required && !f.seen[i] {
							names = append(names, v.display(item.decl.name))
						}
					}
					v.report(CodeContent, f.path, f.line, f.col, "content is incomplete; expected %s", strings.Join(names, ", "))
				}
			} else if state := &model.states[f.state]; !state.final {
				v.report(CodeContent, f.path, f.line, f.col, "content is incomplete; %s", v.expected(state))
			}
		}
		if f.collect || len(f.selfFields) > 0 {
			v.value(f)
		}
		if len(v.scopes) > f.scopeMark || len(v.targets) > 0 || f.inherited != nil {
			v.identityEnd(depth)
		}
	}
	v.ns = v.ns[:f.nsMark]
	f.selfFields, f.seen = nil, nil
	v.frames = v.frames[:depth]
}

// value validates the text of an element with a simple type or simple
// content, and supplies it to identity fields that select the element.
func (v *validator) value(f *frame) {
	typ, decl := f.typ, f.decl
	if typ.simple == nil {
		for _, ref := range f.selfFields {
			cons := ref.tg.sc.c
			v.report(CodeIdentity, f.path, f.line, f.col, "a field of %s %q selects element %q, which has no simple value", cons.word(), cons.name.Local, v.display(f.name))
		}
	}
	if f.nilled || f.hasChild {
		return
	}
	text := string(f.text)
	if typ.simple == nil {
		if decl != nil && decl.fixed != nil && text != *decl.fixed {
			v.report(CodeValue, f.path, f.line, f.col, "value %q does not equal the fixed value %q", shorten(text), *decl.fixed)
		}
		return
	}
	if text == "" && decl != nil {
		if decl.fixed != nil {
			text = *decl.fixed
		} else if decl.def != nil {
			text = *decl.def
		}
	}
	key, kind, reason := typ.simple.validate(text, v)
	if reason != "" {
		v.report(CodeValue, f.path, f.line, f.col, "value %q %s", shorten(text), reason)
		return
	}
	if decl != nil && decl.fixed != nil {
		if fixed, _, r := typ.simple.validate(*decl.fixed, v); r == "" && fixed != key {
			v.report(CodeValue, f.path, f.line, f.col, "value %q does not equal the fixed value %q", shorten(text), *decl.fixed)
		}
	}
	if v.s.hasIDs {
		v.noteID(typ.simple, key, f)
	}
	for _, ref := range f.selfFields {
		v.setField(ref.tg, ref.field, key, kind)
	}
}
