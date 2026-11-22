package xsdlite

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	vAtomic = iota
	vList
	vUnion
)

const (
	wsPreserve = iota
	wsReplace
	wsCollapse
)

// prim is the primitive type a value belongs to. Values of different
// primitives are never equal.
type prim uint8

const (
	pAny prim = iota + 1
	pString
	pBoolean
	pDecimal
	pFloat
	pDouble
	pDuration
	pDateTime
	pTime
	pDate
	pGYearMonth
	pGYear
	pGMonthDay
	pGDay
	pGMonth
	pHexBinary
	pBase64
	pAnyURI
	pQName
	pNotation
)

var primNames = map[prim]string{
	pBoolean: "boolean", pDecimal: "decimal number", pFloat: "float", pDouble: "double", pDuration: "duration",
	pDateTime: "dateTime", pTime: "time", pDate: "date", pGYearMonth: "gYearMonth", pGYear: "gYear",
	pGMonthDay: "gMonthDay", pGDay: "gDay", pGMonth: "gMonth", pHexBinary: "hexBinary value",
	pBase64: "base64Binary value", pQName: "qualified name", pNotation: "notation name", pAnyURI: "URI",
}

const (
	lexLanguage = iota + 1
	lexName
	lexNCName
	lexNMTOKEN
)

const (
	boundMinInclusive = iota
	boundMaxInclusive
	boundMinExclusive
	boundMaxExclusive
)

// simpleType is one step in a chain of simple type derivations. Its facets
// are those the step adds; a value must satisfy every step of the chain.
type simpleType struct {
	name    xname
	td      *typeDef
	base    *simpleType
	variety uint8
	prim    prim
	item    *simpleType
	members []*simpleType
	ws      uint8

	length, minLength, maxLength int
	totalDigits, fractionDigits  int
	patterns                     []*regexp.Regexp
	enums                        map[string]struct{}
	bounds                       [4]*string
	lex                          uint8
	integer                      bool
	id                           uint8 // 1 xs:ID, 2 xs:IDREF
}

func newSimpleType() *simpleType {
	return &simpleType{length: -1, minLength: -1, maxLength: -1, totalDigits: -1, fractionDigits: -1}
}

func (st *simpleType) derive() *simpleType {
	d := newSimpleType()
	d.base, d.variety, d.prim, d.item, d.members, d.ws = st, st.variety, st.prim, st.item, st.members, st.ws
	return d
}

// label names the type for a message: the nearest named type in the chain.
func (st *simpleType) label() string {
	for t := st; t != nil; t = t.base {
		if t.name.Local != "" {
			if t.name.Space == xsdNS {
				return "xs:" + t.name.Local
			}
			return "type " + t.name.Local
		}
	}
	return "its type"
}

// idKind reports whether the type is, or is a list of, xs:ID (1) or xs:IDREF (2).
func (st *simpleType) idKind() uint8 {
	if st.variety == vList && st.item != nil {
		return st.item.idKind()
	}
	for t := st; t != nil; t = t.base {
		if t.id != 0 {
			return t.id
		}
	}
	return 0
}

func isXMLSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

func whitespace(raw string, mode uint8) string {
	if mode == wsPreserve || strings.IndexAny(raw, " \t\n\r") < 0 {
		return raw
	}
	if mode == wsReplace {
		return strings.Map(func(r rune) rune {
			if isXMLSpace(r) {
				return ' '
			}
			return r
		}, raw)
	}
	return strings.Join(strings.FieldsFunc(raw, isXMLSpace), " ")
}

// validate checks a lexical value. It returns the value's canonical form and
// primitive for comparisons, or the reason the value is not valid, phrased to
// follow the value in a sentence.
func (st *simpleType) validate(raw string, ns resolver) (key string, kind prim, reason string) {
	v := whitespace(raw, st.ws)
	units := -1
	switch st.variety {
	case vUnion:
		matched := false
		for _, member := range st.members {
			if k, kd, r := member.validate(raw, ns); r == "" {
				key, kind, matched = k, kd, true
				break
			}
		}
		if !matched {
			return "", 0, "is not valid for " + st.label()
		}
		v = whitespace(raw, wsCollapse)
	case vList:
		items := strings.FieldsFunc(v, isXMLSpace)
		keys := make([]string, len(items))
		for i, item := range items {
			k, kd, r := st.item.validate(item, ns)
			if r != "" {
				return "", 0, "has the list item " + strconv.Quote(item) + ", which " + r
			}
			keys[i], kind = k, kd
		}
		key, units = strings.Join(keys, " "), len(items)
	default:
		k, ok := parsePrimitive(st.prim, v, ns)
		if !ok {
			return "", 0, "is not a valid " + primNames[st.prim]
		}
		key, kind = k, st.prim
	}
	for t := st; t != nil; t = t.base {
		if r := t.check(v, key, units); r != "" {
			return "", 0, r
		}
	}
	return key, kind, ""
}

// check applies the facets of one derivation step.
func (st *simpleType) check(v, key string, units int) string {
	if st.lex != 0 && !lexicalOK(st.lex, v) {
		return "is not a valid " + st.label()
	}
	if st.integer && strings.ContainsAny(v, ".") {
		return "is not a whole number"
	}
	if st.length >= 0 || st.minLength >= 0 || st.maxLength >= 0 {
		n := units
		if n < 0 {
			n = lengthOf(st.prim, v, key)
		}
		switch {
		case n < 0:
		case st.length >= 0 && n != st.length:
			return "must have length " + strconv.Itoa(st.length) + " for " + st.label()
		case st.minLength >= 0 && n < st.minLength:
			if n == 0 {
				return "must not be empty for " + st.label()
			}
			return "is shorter than the minimum length " + strconv.Itoa(st.minLength) + " of " + st.label()
		case st.maxLength >= 0 && n > st.maxLength:
			return "is longer than the maximum length " + strconv.Itoa(st.maxLength) + " of " + st.label()
		}
	}
	if len(st.patterns) > 0 {
		matched := false
		for _, re := range st.patterns {
			if re.MatchString(v) {
				matched = true
				break
			}
		}
		if !matched {
			return "does not match the pattern of " + st.label()
		}
	}
	if st.enums != nil {
		if _, ok := st.enums[key]; !ok {
			return "is not one of the values allowed for " + st.label()
		}
	}
	for i, bound := range st.bounds {
		if bound == nil {
			continue
		}
		cmp, ok := compareKeys(st.prim, key, *bound)
		if !ok {
			continue
		}
		switch {
		case i == boundMinInclusive && cmp < 0, i == boundMinExclusive && cmp <= 0:
			return "is below the minimum of " + st.label()
		case i == boundMaxInclusive && cmp > 0, i == boundMaxExclusive && cmp >= 0:
			return "is above the maximum of " + st.label()
		}
	}
	if st.totalDigits >= 0 || st.fractionDigits >= 0 {
		whole, fraction, _ := strings.Cut(strings.TrimPrefix(key, "-"), ".")
		if st.fractionDigits >= 0 && len(fraction) > st.fractionDigits {
			return "has more than " + strconv.Itoa(st.fractionDigits) + " fraction digits"
		}
		if whole == "0" {
			whole = ""
		}
		if st.totalDigits >= 0 && len(whole)+len(fraction) > st.totalDigits {
			return "has more than " + strconv.Itoa(st.totalDigits) + " digits"
		}
	}
	return ""
}

func lengthOf(p prim, v, key string) int {
	switch p {
	case pHexBinary:
		return len(key) / 2
	case pBase64:
		return len(key)/4*3 - strings.Count(key, "=")
	case pQName, pNotation:
		return -1
	}
	return utf8.RuneCountInString(v)
}

// simpleFrom compiles an xs:simpleType element.
func (c *compiler) simpleFrom(n *node) (*simpleType, error) {
	k := n.first("restriction", "list", "union")
	if k == nil {
		return nil, c.errf(n, "xs:simpleType needs a restriction, a list or a union")
	}
	named := func(value string) (*simpleType, error) {
		td, err := c.typeNamed(k, value)
		if err != nil {
			return nil, err
		}
		if err := c.ensure(td); err != nil {
			return nil, err
		}
		if td.complex || td.simple == nil {
			return nil, c.errf(k, "%s is not a simple type", td.name)
		}
		return td.simple, nil
	}
	switch k.local {
	case "list":
		st := newSimpleType()
		st.variety, st.ws = vList, wsCollapse
		var err error
		if item := k.attrs["itemType"]; item != "" {
			st.item, err = named(item)
		} else if inline := k.first("simpleType"); inline != nil {
			st.item, err = c.simpleFrom(inline)
		} else {
			err = c.errf(k, "xs:list needs an itemType")
		}
		return st, err
	case "union":
		st := newSimpleType()
		st.variety, st.ws = vUnion, wsPreserve
		for _, member := range strings.Fields(k.attrs["memberTypes"]) {
			m, err := named(member)
			if err != nil {
				return nil, err
			}
			st.members = append(st.members, m)
		}
		for _, inline := range k.children {
			if inline.local == "simpleType" {
				m, err := c.simpleFrom(inline)
				if err != nil {
					return nil, err
				}
				st.members = append(st.members, m)
			}
		}
		if len(st.members) == 0 {
			return nil, c.errf(k, "xs:union needs member types")
		}
		return st, nil
	}
	var base *simpleType
	var err error
	if name := k.attrs["base"]; name != "" {
		base, err = named(name)
	} else if inline := k.first("simpleType"); inline != nil {
		base, err = c.simpleFrom(inline)
	} else {
		err = c.errf(k, "xs:restriction needs a base type")
	}
	if err != nil {
		return nil, err
	}
	st := base.derive()
	return st, c.facets(st, k, base)
}

// facets reads the facet elements of a restriction into st.
func (c *compiler) facets(st *simpleType, n *node, base *simpleType) error {
	for _, k := range n.children {
		value := k.attrs["value"]
		number := func() (int, error) {
			i, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || i < 0 {
				return 0, c.errf(k, "xs:%s value %q is not a non-negative whole number", k.local, value)
			}
			return i, nil
		}
		var err error
		switch k.local {
		case "simpleType", "attribute", "attributeGroup", "anyAttribute":
		case "enumeration":
			key, _, reason := base.validate(value, k.ns)
			if reason != "" {
				return c.errf(k, "the enumeration value %q %s", value, reason)
			}
			if st.enums == nil {
				st.enums = map[string]struct{}{}
			}
			st.enums[key] = struct{}{}
		case "pattern":
			re, err := compilePattern(value)
			if err != nil {
				e := c.errf(k, "the pattern %q cannot be used: %v", value, err).(*SchemaError)
				e.Unsupported = true
				return e
			}
			st.patterns = append(st.patterns, re)
		case "length":
			st.length, err = number()
		case "minLength":
			st.minLength, err = number()
		case "maxLength":
			st.maxLength, err = number()
		case "totalDigits":
			st.totalDigits, err = number()
		case "fractionDigits":
			st.fractionDigits, err = number()
		case "whiteSpace":
			switch value {
			case "preserve":
				st.ws = wsPreserve
			case "replace":
				st.ws = wsReplace
			case "collapse":
				st.ws = wsCollapse
			default:
				return c.errf(k, "whiteSpace=%q is not valid", value)
			}
		case "minInclusive", "maxInclusive", "minExclusive", "maxExclusive":
			if st.variety != vAtomic || !ordered(st.prim) {
				return c.unsupported(k, "xs:%s on a type that is not a number, date or time", k.local)
			}
			key, _, reason := base.validate(value, k.ns)
			if reason != "" {
				return c.errf(k, "the %s value %q %s", k.local, value, reason)
			}
			index := map[string]int{"minInclusive": boundMinInclusive, "maxInclusive": boundMaxInclusive, "minExclusive": boundMinExclusive, "maxExclusive": boundMaxExclusive}[k.local]
			st.bounds[index] = &key
		default:
			return c.errf(k, "xs:%s is not a facet", k.local)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// builtins defines the XML Schema built-in types and the xml: attributes.
func (c *compiler) builtins() {
	s := c.s
	any := &typeDef{name: xname{xsdNS, "anyType"}, complex: true, mixed: true, state: 2, attrs: map[xname]*attrUse{}}
	any.particle = &particle{kind: pWild, min: 0, max: -1, wild: &wildcard{any: true, process: processLax}}
	any.anyAttr = &wildcard{any: true, process: processLax}
	c.anyType = any
	s.types[any.name] = any
	c.complexTypes = append(c.complexTypes, any)

	define := func(name string, st *simpleType, base *typeDef) *simpleType {
		st.name = xname{xsdNS, name}
		st.td = &typeDef{name: st.name, simple: st, base: base, state: 2}
		s.types[st.name] = st.td
		c.allTypes = append(c.allTypes, st.td)
		return st
	}
	root := newSimpleType()
	root.prim, root.ws = pAny, wsPreserve
	define("anySimpleType", root, any)
	c.anySimple = root.td

	type row struct {
		name, base string
		prim       prim
		ws         int
		lex        uint8
		integer    bool
		min, max   string
		id         uint8
	}
	rows := []row{
		{name: "string", prim: pString, ws: wsPreserve},
		{name: "normalizedString", base: "string", ws: wsReplace},
		{name: "token", base: "normalizedString", ws: wsCollapse},
		{name: "language", base: "token", ws: -1, lex: lexLanguage},
		{name: "Name", base: "token", ws: -1, lex: lexName},
		{name: "NCName", base: "Name", ws: -1, lex: lexNCName},
		{name: "ID", base: "NCName", ws: -1, id: 1},
		{name: "IDREF", base: "NCName", ws: -1, id: 2},
		{name: "ENTITY", base: "NCName", ws: -1},
		{name: "NMTOKEN", base: "token", ws: -1, lex: lexNMTOKEN},
		{name: "boolean", prim: pBoolean, ws: wsCollapse},
		{name: "decimal", prim: pDecimal, ws: wsCollapse},
		{name: "integer", base: "decimal", ws: -1, integer: true},
		{name: "nonPositiveInteger", base: "integer", ws: -1, max: "0"},
		{name: "negativeInteger", base: "nonPositiveInteger", ws: -1, max: "-1"},
		{name: "long", base: "integer", ws: -1, min: "-9223372036854775808", max: "9223372036854775807"},
		{name: "int", base: "long", ws: -1, min: "-2147483648", max: "2147483647"},
		{name: "short", base: "int", ws: -1, min: "-32768", max: "32767"},
		{name: "byte", base: "short", ws: -1, min: "-128", max: "127"},
		{name: "nonNegativeInteger", base: "integer", ws: -1, min: "0"},
		{name: "unsignedLong", base: "nonNegativeInteger", ws: -1, max: "18446744073709551615"},
		{name: "unsignedInt", base: "unsignedLong", ws: -1, max: "4294967295"},
		{name: "unsignedShort", base: "unsignedInt", ws: -1, max: "65535"},
		{name: "unsignedByte", base: "unsignedShort", ws: -1, max: "255"},
		{name: "positiveInteger", base: "nonNegativeInteger", ws: -1, min: "1"},
		{name: "float", prim: pFloat, ws: wsCollapse}, {name: "double", prim: pDouble, ws: wsCollapse},
		{name: "duration", prim: pDuration, ws: wsCollapse}, {name: "dateTime", prim: pDateTime, ws: wsCollapse},
		{name: "time", prim: pTime, ws: wsCollapse}, {name: "date", prim: pDate, ws: wsCollapse},
		{name: "gYearMonth", prim: pGYearMonth, ws: wsCollapse}, {name: "gYear", prim: pGYear, ws: wsCollapse},
		{name: "gMonthDay", prim: pGMonthDay, ws: wsCollapse}, {name: "gDay", prim: pGDay, ws: wsCollapse},
		{name: "gMonth", prim: pGMonth, ws: wsCollapse}, {name: "hexBinary", prim: pHexBinary, ws: wsCollapse},
		{name: "base64Binary", prim: pBase64, ws: wsCollapse}, {name: "anyURI", prim: pAnyURI, ws: wsCollapse},
		{name: "QName", prim: pQName, ws: wsCollapse}, {name: "NOTATION", prim: pNotation, ws: wsCollapse},
	}
	for _, r := range rows {
		parent := root
		if r.base != "" {
			parent = s.types[xname{xsdNS, r.base}].simple
		}
		st := parent.derive()
		if r.prim != 0 {
			st.prim = r.prim
		}
		if r.ws >= 0 {
			st.ws = uint8(r.ws)
		}
		st.lex, st.integer, st.id = r.lex, r.integer, r.id
		if r.min != "" {
			min := r.min
			st.bounds[boundMinInclusive] = &min
		}
		if r.max != "" {
			max := r.max
			st.bounds[boundMaxInclusive] = &max
		}
		define(r.name, st, parent.td)
	}
	for list, item := range map[string]string{"NMTOKENS": "NMTOKEN", "IDREFS": "IDREF", "ENTITIES": "ENTITY"} {
		st := newSimpleType()
		st.variety, st.ws, st.minLength = vList, wsCollapse, 1
		st.item = s.types[xname{xsdNS, item}].simple
		define(list, st, root.td)
	}
	for name, typ := range map[string]string{"lang": "language", "space": "NCName", "base": "anyURI", "id": "ID"} {
		q := xname{xmlNS, name}
		s.attributes[q] = &attrUse{name: q, typ: s.types[xname{xsdNS, typ}], finished: true}
	}
}
