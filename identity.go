package xsdlite

import (
	"strconv"
	"strings"
)

const (
	cUnique = iota
	cKey
	cKeyref
)

// constraint is an xs:unique, xs:key or xs:keyref.
type constraint struct {
	name      xname
	kind      uint8
	referName xname
	refer     *constraint
	selector  []xpath
	fields    [][]xpath
	owner     *elementDecl
	raw       *node
	// attrFields is set when every field is one attribute of the selected
	// element, which is the usual case and needs no bookkeeping.
	attrFields []xname
	// propagate is set on a key that a keyref on another element refers to,
	// so its values must outlive the element that declares it.
	propagate bool
}

func (c *constraint) word() string {
	return [...]string{"unique constraint", "key", "key reference"}[c.kind]
}

// xpath is one alternative of the restricted XPath that selectors and fields
// use: an optional leading .// and child steps, and for a field an optional
// final attribute step.
type xpath struct {
	descendant bool
	steps      []nameTest
	attr       *nameTest
}

type nameTest struct {
	any      bool // *
	anyLocal bool // prefix:*
	name     xname
}

func (t nameTest) matches(q xname) bool {
	switch {
	case t.any:
		return true
	case t.anyLocal:
		return t.name.Space == q.Space
	}
	return t.name == q
}

func (c *compiler) constraint(owner *elementDecl, n *node) error {
	name := n.attrs["name"]
	if name == "" {
		return c.errf(n, "xs:%s needs a name", n.local)
	}
	cons := &constraint{name: xname{n.doc.target, name}, owner: owner, raw: n}
	cons.kind = map[string]uint8{"unique": cUnique, "key": cKey, "keyref": cKeyref}[n.local]
	if c.s.constraints[cons.name] != nil {
		return c.errf(n, "identity constraint %s is defined twice", cons.name)
	}
	if cons.kind == cKeyref {
		refer, err := c.qname(n, n.attrs["refer"])
		if err != nil {
			return err
		}
		cons.referName = refer
	}
	simple := true
	for _, k := range n.children {
		paths, err := c.xpaths(k, k.attrs["xpath"], k.local == "field")
		if err != nil {
			return err
		}
		switch k.local {
		case "selector":
			cons.selector = paths
		case "field":
			cons.fields = append(cons.fields, paths)
			if len(paths) == 1 && len(paths[0].steps) == 0 && paths[0].attr != nil && !paths[0].attr.any && !paths[0].attr.anyLocal && !paths[0].descendant {
				cons.attrFields = append(cons.attrFields, paths[0].attr.name)
			} else {
				simple = false
			}
		}
	}
	if cons.selector == nil || len(cons.fields) == 0 {
		return c.errf(n, "xs:%s %s needs a selector and at least one field", n.local, name)
	}
	if !simple {
		cons.attrFields = nil
	}
	c.s.constraints[cons.name] = cons
	c.allConstraints = append(c.allConstraints, cons)
	owner.constraints = append(owner.constraints, cons)
	return nil
}

func (c *compiler) xpaths(n *node, expr string, field bool) ([]xpath, error) {
	var out []xpath
	for _, alt := range strings.Split(expr, "|") {
		alt = strings.TrimSpace(alt)
		var x xpath
		if rest, ok := strings.CutPrefix(alt, ".//"); ok {
			x.descendant, alt = true, rest
		}
		if alt == "" {
			return nil, c.errf(n, "the XPath %q is empty", expr)
		}
		steps := strings.Split(alt, "/")
		for i, step := range steps {
			step = strings.TrimPrefix(strings.TrimSpace(step), "child::")
			attribute := false
			if rest, ok := strings.CutPrefix(step, "@"); ok {
				attribute, step = true, rest
			} else if rest, ok := strings.CutPrefix(step, "attribute::"); ok {
				attribute, step = true, rest
			}
			if step == "." && !attribute {
				continue
			}
			test, err := c.nameTest(n, strings.TrimSpace(step))
			if err != nil {
				return nil, err
			}
			if attribute {
				if !field || i != len(steps)-1 {
					return nil, c.errf(n, "the XPath %q uses an attribute where only a field may end with one", expr)
				}
				x.attr = &test
			} else {
				x.steps = append(x.steps, test)
			}
		}
		out = append(out, x)
	}
	return out, nil
}

func (c *compiler) nameTest(n *node, step string) (nameTest, error) {
	if step == "*" {
		return nameTest{any: true}, nil
	}
	prefix, local, found := strings.Cut(step, ":")
	if !found {
		prefix, local = "", step
	}
	if local != "*" && !isNCName(local) {
		e := c.errf(n, "the XPath step %q is outside the subset that identity constraints allow", step).(*SchemaError)
		e.Unsupported = true
		return nameTest{}, e
	}
	test := nameTest{name: xname{"", local}, anyLocal: local == "*"}
	if found {
		uri, ok := n.ns.resolvePrefix(prefix)
		if !ok {
			return nameTest{}, c.errf(n, "the namespace prefix %q is not declared", prefix)
		}
		test.name.Space = uri
	}
	return test, nil
}

func (c *compiler) finishConstraints() error {
	for _, cons := range c.allConstraints {
		if cons.kind != cKeyref {
			continue
		}
		refer := c.s.constraints[cons.referName]
		if refer == nil || refer.kind == cKeyref {
			return c.errf(cons.raw, "key reference %s refers to %s, which is not a key or unique constraint", cons.name.Local, cons.referName)
		}
		if len(refer.fields) != len(cons.fields) {
			return c.errf(cons.raw, "key reference %s has %d fields but %s has %d", cons.name.Local, len(cons.fields), refer.name.Local, len(refer.fields))
		}
		cons.refer = refer
		if refer.owner != cons.owner {
			refer.propagate = true
		}
	}
	return nil
}

// Runtime state.

// scope is one identity constraint in force under one element.
type scope struct {
	c     *constraint
	depth int
	table map[string]int32 // unique and key: value to line
	rows  []keyrefRow
}

type keyrefRow struct {
	tuple     string
	path      int32
	line, col int32
}

// target is an element a selector matched, while its fields are gathered.
type target struct {
	sc        *scope
	depth     int
	values    []string
	have      []bool
	multiple  bool
	path      int32
	line, col int
}

type fieldRef struct {
	tg    *target
	field int
}

const tupleSeparator = "\x1f"

// fieldValue tags a canonical value with its primitive type, so that equal
// text of unrelated types does not compare equal.
func fieldValue(key string, kind prim) string {
	if kind == pAny {
		kind = pString
	}
	return string(rune('A'+kind)) + key
}

func displayTuple(tuple string) string {
	parts := strings.Split(tuple, tupleSeparator)
	for i, part := range parts {
		if part != "" {
			part = part[1:]
		}
		parts[i] = strconv.Quote(shorten(part))
	}
	return strings.Join(parts, ", ")
}

func (v *validator) matchPath(x *xpath, from, depth int) bool {
	n := len(x.steps)
	rel := depth - from
	if n == 0 || rel < n || (!x.descendant && rel != n) {
		return false
	}
	for i := 0; i < n; i++ {
		if !x.steps[n-1-i].matches(v.frames[depth-i].name) {
			return false
		}
	}
	return true
}

// identityStart runs when an element has been opened and its attributes
// validated. depth is the element's index in the frame stack.
func (v *validator) identityStart(depth int) {
	f := &v.frames[depth]
	// Fields of enclosing targets that reach down to this element.
	for _, tg := range v.targets {
		for i, alts := range tg.sc.c.fields {
			for a := range alts {
				x := &alts[a]
				if !v.matchPath(x, tg.depth, depth) {
					continue
				}
				if x.attr != nil {
					v.attrField(tg, i, x.attr)
				} else {
					f.selfFields = append(f.selfFields, fieldRef{tg, i})
				}
			}
		}
	}
	// Selectors of the constraints in force.
	for _, sc := range v.scopes {
		for a := range sc.c.selector {
			if v.matchPath(&sc.c.selector[a], sc.depth, depth) {
				v.selected(sc, depth)
				break
			}
		}
	}
	// Constraints this element declares.
	if f.decl == nil {
		return
	}
	for _, cons := range f.decl.constraints {
		sc := &scope{c: cons, depth: depth}
		if cons.kind != cKeyref {
			sc.table = map[string]int32{}
		}
		v.scopes = append(v.scopes, sc)
		for a := range cons.selector {
			if x := &cons.selector[a]; len(x.steps) == 0 && !x.descendant {
				v.selected(sc, depth)
				break
			}
		}
	}
}

// selected handles an element that a selector matched.
func (v *validator) selected(sc *scope, depth int) {
	f := &v.frames[depth]
	cons := sc.c
	if cons.attrFields != nil {
		v.tuple = v.tuple[:0]
		for i, name := range cons.attrFields {
			found := false
			for j := range v.attrs {
				if v.attrs[j].name == name {
					if v.attrs[j].kind == 0 {
						return
					}
					if i > 0 {
						v.tuple = append(v.tuple, tupleSeparator...)
					}
					v.tuple = append(v.tuple, fieldValue(v.attrs[j].key, v.attrs[j].kind)...)
					found = true
					break
				}
			}
			if !found {
				// A missing required attribute has already been reported.
				if cons.kind == cKey && len(v.result.Violations) == f.reported {
					v.report(CodeIdentity, f.path, f.line, f.col, "key %q needs attribute %q, which is missing", cons.name.Local, name.Local)
				}
				return
			}
		}
		v.addRow(sc, v.tuple, f.path, f.line, f.col)
		return
	}
	tg := &target{sc: sc, depth: depth, values: make([]string, len(cons.fields)), have: make([]bool, len(cons.fields)), path: f.path, line: f.line, col: f.col}
	v.targets = append(v.targets, tg)
	for i, alts := range cons.fields {
		for a := range alts {
			x := &alts[a]
			if len(x.steps) != 0 || x.descendant {
				continue
			}
			if x.attr != nil {
				v.attrField(tg, i, x.attr)
			} else {
				f.selfFields = append(f.selfFields, fieldRef{tg, i})
			}
		}
	}
}

func (v *validator) attrField(tg *target, field int, test *nameTest) {
	for j := range v.attrs {
		if v.attrs[j].kind != 0 && test.matches(v.attrs[j].name) {
			v.setField(tg, field, v.attrs[j].key, v.attrs[j].kind)
		}
	}
}

func (v *validator) setField(tg *target, field int, key string, kind prim) {
	if tg.have[field] {
		tg.multiple = true
		return
	}
	tg.have[field], tg.values[field] = true, fieldValue(key, kind)
}

func (v *validator) addRow(sc *scope, tuple []byte, path int32, line, col int) {
	if sc.c.kind == cKeyref {
		sc.rows = append(sc.rows, keyrefRow{tuple: string(tuple), path: path, line: int32(line), col: int32(col)})
		return
	}
	if first, dup := sc.table[string(tuple)]; dup {
		v.report(CodeIdentity, path, line, col, "duplicate value %s for %s %q; it is first used on line %d", displayTuple(string(tuple)), sc.c.word(), sc.c.name.Local, first)
		return
	}
	sc.table[string(tuple)] = int32(line)
}

// identityEnd runs when the element at depth closes.
func (v *validator) identityEnd(depth int) {
	f := &v.frames[depth]
	// Targets that are this element have all the field values they will get.
	for len(v.targets) > 0 && v.targets[len(v.targets)-1].depth == depth {
		tg := v.targets[len(v.targets)-1]
		v.targets = v.targets[:len(v.targets)-1]
		cons := tg.sc.c
		complete := true
		for _, have := range tg.have {
			complete = complete && have
		}
		switch {
		case tg.multiple:
			v.report(CodeIdentity, tg.path, tg.line, tg.col, "a field of %s %q selects more than one value", cons.word(), cons.name.Local)
		case complete:
			v.addRow(tg.sc, []byte(strings.Join(tg.values, tupleSeparator)), tg.path, tg.line, tg.col)
		case cons.kind == cKey:
			v.report(CodeIdentity, tg.path, tg.line, tg.col, "key %q has a field with no value", cons.name.Local)
		}
	}
	own := v.scopes[f.scopeMark:]
	for _, sc := range own {
		if sc.c.kind != cKeyref {
			continue
		}
		var table map[string]int32
		for _, other := range own {
			if other.c == sc.c.refer {
				table = other.table
			}
		}
		inherited := f.inherited[sc.c.refer]
		for _, row := range sc.rows {
			if _, ok := table[row.tuple]; ok {
				continue
			}
			if _, ok := inherited[row.tuple]; ok {
				continue
			}
			v.report(CodeIdentity, row.path, int(row.line), int(row.col), "value %s has no matching %q key (key reference %q)", displayTuple(row.tuple), sc.c.refer.name.Local, sc.c.name.Local)
		}
	}
	// Hand keys that an outer keyref needs to the parent element.
	if depth > 0 {
		parent := &v.frames[depth-1]
		for _, sc := range own {
			if sc.c.propagate {
				parent.inherit(sc.c, sc.table)
			}
		}
		for cons, table := range f.inherited {
			parent.inherit(cons, table)
		}
	}
	f.inherited = nil
	v.scopes = v.scopes[:f.scopeMark]
}

func (f *frame) inherit(cons *constraint, table map[string]int32) {
	if len(table) == 0 {
		return
	}
	if f.inherited == nil {
		f.inherited = map[*constraint]map[string]int32{}
	}
	have := f.inherited[cons]
	if have == nil {
		f.inherited[cons] = table
		return
	}
	if len(have) < len(table) {
		have, table = table, have
		f.inherited[cons] = have
	}
	for k, line := range table {
		have[k] = line
	}
}
