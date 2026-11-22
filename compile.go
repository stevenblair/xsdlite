package xsdlite

import (
	"sort"
	"strconv"
	"strings"
)

// typeDef is a simple or complex type definition.
type typeDef struct {
	name     xname
	base     *typeDef
	complex  bool
	abstract bool
	// simple is the simple type itself, or the simple content of a complex
	// type. It is nil for a complex type with element or empty content.
	simple *simpleType

	// Complex types only.
	mixed    bool
	particle *particle
	model    *contentModel
	attrs    map[xname]*attrUse
	required []*attrUse
	defaults []*attrUse
	anyAttr  *wildcard

	raw   *node
	state uint8 // 0 not compiled, 1 compiling, 2 compiled
}

func (t *typeDef) label() string {
	switch {
	case t.name.Local == "":
		return "an anonymous type"
	case t.name.Space == xsdNS:
		return "xs:" + t.name.Local
	}
	return t.name.Local
}

// derivesFrom reports whether t is base or is derived from it.
func (t *typeDef) derivesFrom(base *typeDef) bool {
	for d := t; d != nil; d = d.base {
		if d == base {
			return true
		}
	}
	// A member type of a union is validly derived from the union.
	if base.simple != nil && base.simple.variety == vUnion && t.simple != nil {
		for _, member := range base.simple.members {
			if member.td != nil && t.derivesFrom(member.td) {
				return true
			}
		}
	}
	return false
}

type attrUse struct {
	name       xname
	typ        *typeDef
	required   bool
	fixed, def *string
	// Canonical form of fixed or def, filled in when the schema is finished.
	key      string
	kind     prim
	finished bool
	raw      *node
}

type elementDecl struct {
	name                       xname
	typ                        *typeDef
	nillable, abstract, global bool
	fixed, def                 *string
	constraints                []*constraint
}

// wildcard is xs:any or xs:anyAttribute.
type wildcard struct {
	any     bool
	other   bool   // ##other: any namespace except notNS and no namespace
	notNS   string // for other
	list    []string
	process uint8 // 0 strict, 1 lax, 2 skip
}

const (
	processStrict = iota
	processLax
	processSkip
)

func (w *wildcard) allows(space string) bool {
	switch {
	case w.any:
		return true
	case w.other:
		return space != "" && space != w.notNS
	}
	for _, s := range w.list {
		if s == space {
			return true
		}
	}
	return false
}

// unionWildcard combines the attribute wildcards of a base type and its
// extension. Combinations that XML Schema cannot express become "any".
func unionWildcard(a, b *wildcard) *wildcard {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	out := &wildcard{process: b.process}
	switch {
	case a.any || b.any:
		out.any = true
	case a.other && b.other:
		if a.notNS == b.notNS {
			out.other, out.notNS = true, a.notNS
		} else {
			out.any = true
		}
	case a.other || b.other:
		out.any = true
	default:
		out.list = append(append([]string{}, a.list...), b.list...)
	}
	return out
}

func (c *compiler) wildcard(n *node) (*wildcard, error) {
	w := &wildcard{}
	switch strings.TrimSpace(n.attrs["processContents"]) {
	case "", "strict":
	case "lax":
		w.process = processLax
	case "skip":
		w.process = processSkip
	default:
		return nil, c.errf(n, "processContents=%q is not valid", n.attrs["processContents"])
	}
	spaces, ok := n.attrs["namespace"]
	switch strings.TrimSpace(spaces) {
	case "##any":
		w.any = true
	case "##other":
		w.other, w.notNS = true, n.doc.target
	default:
		if !ok {
			w.any = true
			break
		}
		for _, item := range strings.Fields(spaces) {
			switch item {
			case "##targetNamespace":
				w.list = append(w.list, n.doc.target)
			case "##local":
				w.list = append(w.list, "")
			default:
				if strings.HasPrefix(item, "##") {
					return nil, c.errf(n, "namespace=%q is not valid: %s cannot be part of a list", spaces, item)
				}
				w.list = append(w.list, item)
			}
		}
	}
	return w, nil
}

func (c *compiler) compile() (*Schema, error) {
	s := c.s
	c.builtins()
	for _, q := range sortedNames(c.rawTypes) {
		if q.Space == xsdNS {
			return nil, c.errf(c.rawTypes[q], "a schema may not define types in the XML Schema namespace")
		}
		s.types[q] = &typeDef{name: q, raw: c.rawTypes[q]}
	}
	for _, q := range sortedNames(c.rawElements) {
		s.elements[q] = &elementDecl{name: q, global: true}
	}
	for _, q := range sortedNames(c.rawElements) {
		if head := c.rawElements[q].attrs["substitutionGroup"]; head != "" {
			h, err := c.qname(c.rawElements[q], head)
			if err != nil {
				return nil, err
			}
			if s.elements[h] == nil {
				return nil, c.errf(c.rawElements[q], "substitution group head %s is not declared", h)
			}
			c.subst[h] = append(c.subst[h], q)
		}
	}
	for _, q := range sortedNames(c.rawAttrs) {
		use, _, err := c.attributeUse(c.rawAttrs[q], true)
		if err != nil {
			return nil, err
		}
		s.attributes[q] = use
	}
	for _, q := range sortedNames(c.rawTypes) {
		if err := c.ensure(s.types[q]); err != nil {
			return nil, err
		}
	}
	for _, q := range sortedNames(c.rawElements) {
		if err := c.element(s.elements[q], c.rawElements[q]); err != nil {
			return nil, err
		}
	}
	for len(c.pending) > 0 {
		next := c.pending[0]
		c.pending = c.pending[1:]
		if err := next(); err != nil {
			return nil, err
		}
	}
	if err := c.finishAttributes(); err != nil {
		return nil, err
	}
	for _, td := range c.complexTypes {
		model, err := c.buildModel(td)
		if err != nil {
			return nil, err
		}
		td.model = model
	}
	if err := c.finishConstraints(); err != nil {
		return nil, err
	}
	s.anyType = c.anyType
	for _, td := range c.allTypes {
		if td.simple != nil && td.simple.idKind() != 0 {
			s.hasIDs = true
		}
	}
	return s, nil
}

func (c *compiler) typeNamed(n *node, value string) (*typeDef, error) {
	q, err := c.qname(n, value)
	if err != nil {
		return nil, err
	}
	td := c.s.types[q]
	if td == nil {
		return nil, c.errf(n, "type %s is not defined", q)
	}
	return td, nil
}

// ensure compiles a named or anonymous type on first use.
func (c *compiler) ensure(td *typeDef) error {
	switch td.state {
	case 2:
		return nil
	case 1:
		return c.errf(td.raw, "type %s is defined in terms of itself", td.name)
	}
	td.state = 1
	c.allTypes = append(c.allTypes, td)
	var err error
	if td.raw.local == "simpleType" {
		var st *simpleType
		if st, err = c.simpleFrom(td.raw); err == nil {
			st.name, st.td = td.name, td
			td.simple = st
			td.base = c.anySimple
			if st.base != nil && st.base.td != nil {
				td.base = st.base.td
			}
		}
	} else {
		err = c.complexType(td, td.raw)
	}
	td.state = 2
	return err
}

func (c *compiler) complexType(td *typeDef, n *node) error {
	td.complex = true
	td.abstract = n.flag("abstract")
	td.attrs = map[xname]*attrUse{}
	c.complexTypes = append(c.complexTypes, td)
	mixed := n.flag("mixed")
	content, derivation, base := n, "", c.anyType
	for _, k := range n.children {
		switch k.local {
		case "simpleContent":
			return c.simpleContent(td, k)
		case "complexContent":
			if _, ok := k.attrs["mixed"]; ok {
				mixed = k.flag("mixed")
			}
			d := k.first("restriction", "extension")
			if d == nil {
				return c.errf(k, "xs:complexContent needs a restriction or an extension")
			}
			b, err := c.typeNamed(d, d.attrs["base"])
			if err != nil {
				return err
			}
			if err := c.ensure(b); err != nil {
				return err
			}
			if !b.complex {
				return c.errf(d, "complex content cannot be derived from the simple type %s", b.name)
			}
			content, derivation, base = d, d.local, b
		}
	}
	td.base = base
	var own *particle
	if k := content.first("sequence", "choice", "all", "group"); k != nil {
		var err error
		if own, err = c.particle(k); err != nil {
			return err
		}
	}
	if derivation == "extension" {
		if base.simple != nil {
			return c.errf(content, "complex content cannot extend %s, which has simple content", base.name)
		}
		switch {
		case base.particle == nil:
			td.particle = own
		case own == nil:
			td.particle = base.particle
		case base.particle.kind == pAll || own.kind == pAll:
			return c.unsupported(content, "extending an xs:all group")
		default:
			td.particle = &particle{kind: pSeq, min: 1, max: 1, children: []*particle{base.particle, own}}
		}
		td.mixed = mixed || base.mixed
		for q, use := range base.attrs {
			td.attrs[q] = use
		}
		td.anyAttr = base.anyAttr
	} else {
		td.particle, td.mixed = own, mixed
		if derivation == "restriction" && base != c.anyType {
			for q, use := range base.attrs {
				td.attrs[q] = use
			}
		}
	}
	return c.attributes(td, content, derivation != "extension", 0)
}

func (c *compiler) simpleContent(td *typeDef, k *node) error {
	d := k.first("restriction", "extension")
	if d == nil {
		return c.errf(k, "xs:simpleContent needs a restriction or an extension")
	}
	base, err := c.typeNamed(d, d.attrs["base"])
	if err != nil {
		return err
	}
	if err := c.ensure(base); err != nil {
		return err
	}
	if base.simple == nil {
		return c.errf(d, "simple content cannot be derived from %s, which has no simple content", base.name)
	}
	td.base, td.simple = base, base.simple
	if base.complex {
		for q, use := range base.attrs {
			td.attrs[q] = use
		}
		td.anyAttr = base.anyAttr
	}
	if d.local == "restriction" {
		if !base.complex {
			return c.errf(d, "simple content can only restrict a complex type")
		}
		from := base.simple
		if inline := d.first("simpleType"); inline != nil {
			if from, err = c.simpleFrom(inline); err != nil {
				return err
			}
		}
		st := from.derive()
		if err := c.facets(st, d, from); err != nil {
			return err
		}
		td.simple = st
		td.anyAttr = nil
	}
	return c.attributes(td, d, d.local == "restriction", 0)
}

// attributes adds the attribute declarations under n to td.
func (c *compiler) attributes(td *typeDef, n *node, replaceWildcard bool, depth int) error {
	if depth > 40 {
		return c.errf(n, "attribute groups refer to each other in a loop")
	}
	for _, k := range n.children {
		switch k.local {
		case "attribute":
			use, prohibited, err := c.attributeUse(k, false)
			if err != nil {
				return err
			}
			if prohibited {
				delete(td.attrs, use.name)
			} else {
				td.attrs[use.name] = use
			}
		case "attributeGroup":
			if depth == 0 || k.attrs["ref"] != "" {
				q, err := c.qname(k, k.attrs["ref"])
				if err != nil {
					return err
				}
				group := c.rawAttrGroups[q]
				if group == nil {
					return c.errf(k, "attribute group %s is not defined", q)
				}
				if err := c.attributes(td, group, replaceWildcard, depth+1); err != nil {
					return err
				}
			}
		case "anyAttribute":
			w, err := c.wildcard(k)
			if err != nil {
				return err
			}
			if replaceWildcard && depth == 0 {
				td.anyAttr = w
			} else {
				td.anyAttr = unionWildcard(td.anyAttr, w)
			}
		}
	}
	return nil
}

func (c *compiler) attributeUse(n *node, global bool) (*attrUse, bool, error) {
	use := &attrUse{raw: n}
	if ref := n.attrs["ref"]; ref != "" && !global {
		q, err := c.qname(n, ref)
		if err != nil {
			return nil, false, err
		}
		decl := c.s.attributes[q]
		if decl == nil {
			return nil, false, c.errf(n, "attribute %s is not declared", q)
		}
		*use = *decl
		use.raw, use.finished = n, false
	} else {
		name := n.attrs["name"]
		if name == "" {
			return nil, false, c.errf(n, "an attribute needs a name or a ref")
		}
		use.name = xname{"", name}
		if form, ok := n.attrs["form"]; global || form == "qualified" || (!ok && n.doc.attrQualified) {
			use.name.Space = n.doc.target
		}
		switch inline := n.first("simpleType"); {
		case n.attrs["type"] != "":
			td, err := c.typeNamed(n, n.attrs["type"])
			if err != nil {
				return nil, false, err
			}
			use.typ = td
		case inline != nil:
			use.typ = &typeDef{raw: inline}
			if err := c.ensure(use.typ); err != nil {
				return nil, false, err
			}
		default:
			use.typ = c.anySimple
		}
	}
	if v, ok := n.attrs["fixed"]; ok {
		use.fixed, use.def = &v, nil
	}
	if v, ok := n.attrs["default"]; ok {
		use.def = &v
	}
	switch n.attrs["use"] {
	case "required":
		use.required = true
	case "prohibited":
		return use, true, nil
	}
	c.attrUses = append(c.attrUses, use)
	return use, false, nil
}

// finishAttributes checks attribute types and constraint values once every
// type is compiled, and lists each type's required and defaulted attributes.
func (c *compiler) finishAttributes() error {
	for _, use := range c.attrUses {
		if use.finished {
			continue
		}
		use.finished = true
		if err := c.ensure(use.typ); err != nil {
			return err
		}
		if use.typ.complex || use.typ.simple == nil {
			return c.errf(use.raw, "attribute %s has the complex type %s", use.name, use.typ.name)
		}
		value := use.fixed
		if value == nil {
			value = use.def
		}
		if value != nil {
			key, kind, reason := use.typ.simple.validate(*value, use.raw.ns)
			if reason != "" {
				return c.errf(use.raw, "the default or fixed value %q of attribute %s %s", *value, use.name, reason)
			}
			use.key, use.kind = key, kind
		}
	}
	for _, td := range c.complexTypes {
		for _, q := range sortedNames(td.attrs) {
			use := td.attrs[q]
			if use.required {
				td.required = append(td.required, use)
			} else if use.def != nil || use.fixed != nil {
				td.defaults = append(td.defaults, use)
			}
		}
	}
	return nil
}

func (c *compiler) occurs(n *node) (min, max int, err error) {
	min, max = 1, 1
	if v, ok := n.attrs["minOccurs"]; ok {
		if min, err = strconv.Atoi(strings.TrimSpace(v)); err != nil || min < 0 {
			return 0, 0, c.errf(n, "minOccurs=%q is not valid", v)
		}
	}
	if v, ok := n.attrs["maxOccurs"]; ok {
		if strings.TrimSpace(v) == "unbounded" {
			max = -1
		} else if max, err = strconv.Atoi(strings.TrimSpace(v)); err != nil || max < 0 {
			return 0, 0, c.errf(n, "maxOccurs=%q is not valid", v)
		}
	}
	if max >= 0 && max < min {
		return 0, 0, c.errf(n, "maxOccurs is less than minOccurs")
	}
	return min, max, nil
}

func (c *compiler) particle(n *node) (*particle, error) {
	min, max, err := c.occurs(n)
	if err != nil {
		return nil, err
	}
	p := &particle{min: min, max: max}
	switch n.local {
	case "element":
		p.kind = pElem
		if ref := n.attrs["ref"]; ref != "" {
			q, err := c.qname(n, ref)
			if err != nil {
				return nil, err
			}
			if p.decl = c.s.elements[q]; p.decl == nil {
				return nil, c.errf(n, "element %s is not declared", q)
			}
			break
		}
		if p.decl = c.localElems[n]; p.decl == nil {
			name := n.attrs["name"]
			if name == "" {
				return nil, c.errf(n, "an element needs a name or a ref")
			}
			decl := &elementDecl{name: xname{"", name}}
			if form, ok := n.attrs["form"]; form == "qualified" || (!ok && n.doc.elemQualified) {
				decl.name.Space = n.doc.target
			}
			c.localElems[n] = decl
			if err := c.element(decl, n); err != nil {
				return nil, err
			}
			p.decl = decl
		}
	case "any":
		p.kind = pWild
		if p.wild, err = c.wildcard(n); err != nil {
			return nil, err
		}
	case "sequence", "choice", "all":
		p.kind = map[string]uint8{"sequence": pSeq, "choice": pChoice, "all": pAll}[n.local]
		for _, k := range n.children {
			child, err := c.particle(k)
			if err != nil {
				return nil, err
			}
			if p.kind == pAll && (child.kind != pElem || child.max > 1) {
				return nil, c.errf(k, "an xs:all group may only hold elements that occur at most once")
			}
			if child.kind == pAll {
				return nil, c.errf(k, "an xs:all group cannot be nested in another group")
			}
			p.children = append(p.children, child)
		}
		if p.kind == pAll && (p.max != 1 || p.min > 1) {
			return nil, c.errf(n, "an xs:all group must occur once or be optional")
		}
	case "group":
		q, err := c.qname(n, n.attrs["ref"])
		if err != nil {
			return nil, err
		}
		group := c.rawGroups[q]
		if group == nil {
			return nil, c.errf(n, "group %s is not defined", q)
		}
		body := group.first("sequence", "choice", "all")
		if body == nil {
			return nil, c.errf(group, "group %s has no content", q)
		}
		if c.groupDepth++; c.groupDepth > 40 {
			return nil, c.errf(n, "group %s refers to itself", q)
		}
		inner, err := c.particle(body)
		c.groupDepth--
		if err != nil {
			return nil, err
		}
		if inner.kind == pAll {
			if min > 1 || max != 1 {
				return nil, c.errf(n, "an xs:all group must occur once or be optional")
			}
			inner.min = min
			return inner, nil
		}
		p.kind, p.children = pSeq, []*particle{inner}
	default:
		return nil, c.errf(n, "xs:%s is not allowed in a content model", n.local)
	}
	return p, nil
}

func (c *compiler) element(decl *elementDecl, n *node) error {
	decl.nillable, decl.abstract = n.flag("nillable"), n.flag("abstract")
	if v, ok := n.attrs["fixed"]; ok {
		decl.fixed = &v
	} else if v, ok := n.attrs["default"]; ok {
		decl.def = &v
	}
	inline := n.first("complexType", "simpleType")
	switch {
	case n.attrs["type"] != "":
		td, err := c.typeNamed(n, n.attrs["type"])
		if err != nil {
			return err
		}
		decl.typ = td
	case inline != nil:
		// An anonymous type may refer back to the type that contains this
		// element, so it is compiled after its surroundings.
		td := &typeDef{raw: inline}
		decl.typ = td
		c.pending = append(c.pending, func() error { return c.ensure(td) })
	case n.attrs["substitutionGroup"] != "":
		decl.typ = c.anyType
		head, err := c.qname(n, n.attrs["substitutionGroup"])
		if err != nil {
			return err
		}
		c.pending = append(c.pending, func() error {
			if h := c.s.elements[head]; h != nil && h.typ != nil {
				decl.typ = h.typ
			}
			return nil
		})
	default:
		decl.typ = c.anyType
	}
	for _, k := range n.children {
		switch k.local {
		case "unique", "key", "keyref":
			if err := c.constraint(decl, k); err != nil {
				return err
			}
		}
	}
	return nil
}

// substitutes lists the elements that may stand in for head, itself included
// unless it is abstract.
func (c *compiler) substitutes(head *elementDecl, seen map[xname]bool) []*elementDecl {
	var out []*elementDecl
	if seen[head.name] {
		return nil
	}
	seen[head.name] = true
	if !head.abstract {
		out = append(out, head)
	}
	if head.global {
		members := c.subst[head.name]
		sort.Slice(members, func(i, j int) bool { return members[i].Local < members[j].Local })
		for _, q := range members {
			out = append(out, c.substitutes(c.s.elements[q], seen)...)
		}
	}
	return out
}
