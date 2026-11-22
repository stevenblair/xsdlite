package xsdlite

const (
	pElem = iota + 1
	pWild
	pSeq
	pChoice
	pAll
)

// particle is a term of a content model with its occurrence range.
type particle struct {
	kind     uint8
	min, max int // max -1 is unbounded
	decl     *elementDecl
	wild     *wildcard
	children []*particle
}

// maxPositions bounds the automaton built for one type. A schema reaches it
// only by multiplying large occurrence counts.
const maxPositions = 60000

// contentModel decides which child elements may follow one another. Schemas
// must be deterministic (the Unique Particle Attribution rule), so a position
// automaton needs no backtracking: each state maps a child name to one next
// state. An xs:all group is checked as a set instead.
type contentModel struct {
	states []modelState // state 0 is the start; state i+1 is position i
	// all-group form
	all         bool
	allIndex    map[xname]int
	allItems    []allItem
	allOptional bool
	// every element declared anywhere in the model, for error recovery
	declared map[xname]*elementDecl
}

type modelState struct {
	next  map[xname]transition
	wild  []wildTransition
	order []xname // element names in schema order, for messages
	final bool
}

type transition struct {
	state int32
	decl  *elementDecl
}

type wildTransition struct {
	state int32
	wild  *wildcard
}

type allItem struct {
	decl     *elementDecl
	required bool
}

// glushkov holds the position sets used to build the automaton.
type glushkov struct {
	c       *compiler
	td      *typeDef
	leaves  []*particle
	follow  [][]int32
	tooMany bool
}

type fragment struct {
	nullable    bool
	first, last []int32
}

func (c *compiler) buildModel(td *typeDef) (*contentModel, error) {
	p := td.particle
	if p == nil {
		return nil, nil
	}
	m := &contentModel{declared: map[xname]*elementDecl{}}
	collectDeclared(p, m.declared, c, 0)
	if p.kind == pAll {
		m.all, m.allIndex, m.allOptional = true, map[xname]int{}, p.min == 0
		for _, child := range p.children {
			for _, decl := range c.substitutes(child.decl, map[xname]bool{}) {
				m.allIndex[decl.name] = len(m.allItems)
			}
			m.allItems = append(m.allItems, allItem{decl: child.decl, required: child.min > 0})
		}
		if len(p.children) == 0 {
			return nil, nil
		}
		return m, nil
	}
	g := &glushkov{c: c, td: td}
	root := g.expand(p)
	if g.tooMany {
		return nil, c.unsupported(td.raw, "the content model of %s is too large", td.label())
	}
	if len(g.leaves) == 0 {
		return nil, nil
	}
	m.states = make([]modelState, len(g.leaves)+1)
	last := map[int32]bool{}
	for _, pos := range root.last {
		last[pos] = true
	}
	for i := range m.states {
		targets := root.first
		final := root.nullable
		if i > 0 {
			targets, final = g.follow[i-1], last[int32(i-1)]
		}
		state := &m.states[i]
		state.final = final
		// The schema must be deterministic: no child may match two different
		// particles at one point (Unique Particle Attribution).
		from := map[xname]*particle{}
		ambiguous := func(name string) error {
			return c.errf(td.raw, "the content model of %s is ambiguous: at one point %s can match two different particles", td.label(), name)
		}
		for _, pos := range targets {
			leaf := g.leaves[pos]
			if leaf.kind == pWild {
				for _, other := range state.wild {
					if other.wild != leaf.wild && wildcardsOverlap(other.wild, leaf.wild) {
						return nil, ambiguous("an element of a wildcard namespace")
					}
				}
				state.wild = append(state.wild, wildTransition{state: pos + 1, wild: leaf.wild})
				continue
			}
			for _, decl := range c.substitutes(leaf.decl, map[xname]bool{}) {
				if other, taken := from[decl.name]; taken {
					if other != leaf {
						return nil, ambiguous("element " + decl.name.Local)
					}
					continue
				}
				from[decl.name] = leaf
				if state.next == nil {
					state.next = map[xname]transition{}
				}
				state.next[decl.name] = transition{state: pos + 1, decl: decl}
				state.order = append(state.order, decl.name)
			}
		}
	}
	if name, overlap := m.checkWildcards(); overlap {
		return nil, c.errf(td.raw, "the content model of %s is ambiguous: at one point element %s can match both its declaration and a wildcard", td.label(), name.Local)
	}
	return m, nil
}

// checkWildcards reports an element that a wildcard of the same state also
// accepts.
func (m *contentModel) checkWildcards() (xname, bool) {
	for i := range m.states {
		for name := range m.states[i].next {
			for _, w := range m.states[i].wild {
				if w.wild.allows(name.Space) {
					return name, true
				}
			}
		}
	}
	return xname{}, false
}

func wildcardsOverlap(a, b *wildcard) bool {
	switch {
	case a.any || b.any:
		return true
	case a.other && b.other:
		return true
	case a.other:
		a, b = b, a
	}
	for _, space := range a.list {
		if b.allows(space) {
			return true
		}
	}
	return false
}

func collectDeclared(p *particle, into map[xname]*elementDecl, c *compiler, depth int) {
	if p.kind == pElem {
		for _, decl := range c.substitutes(p.decl, map[xname]bool{}) {
			if into[decl.name] == nil {
				into[decl.name] = decl
			}
		}
	}
	for _, child := range p.children {
		collectDeclared(child, into, c, depth+1)
	}
}

func (g *glushkov) position(p *particle) fragment {
	if len(g.leaves) >= maxPositions {
		g.tooMany = true
		return fragment{nullable: true}
	}
	pos := int32(len(g.leaves))
	g.leaves = append(g.leaves, p)
	g.follow = append(g.follow, nil)
	return fragment{first: []int32{pos}, last: []int32{pos}}
}

func (g *glushkov) link(from, to []int32) {
	for _, pos := range from {
		g.follow[pos] = union(g.follow[pos], to)
	}
}

func union(a, b []int32) []int32 {
	if len(a) == 0 {
		return append([]int32(nil), b...)
	}
	for _, x := range b {
		found := false
		for _, y := range a {
			if x == y {
				found = true
				break
			}
		}
		if !found {
			a = append(a, x)
		}
	}
	return a
}

func (g *glushkov) seq(a, b fragment) fragment {
	g.link(a.last, b.first)
	out := fragment{nullable: a.nullable && b.nullable, first: a.first, last: b.last}
	if a.nullable {
		out.first = union(append([]int32(nil), a.first...), b.first)
	}
	if b.nullable {
		out.last = union(append([]int32(nil), b.last...), a.last)
	}
	return out
}

// term builds one occurrence of p, with fresh positions.
func (g *glushkov) term(p *particle) fragment {
	switch p.kind {
	case pElem, pWild:
		return g.position(p)
	case pChoice:
		out := fragment{nullable: len(p.children) == 0}
		for _, child := range p.children {
			f := g.expand(child)
			out.nullable = out.nullable || f.nullable
			out.first = union(out.first, f.first)
			out.last = union(out.last, f.last)
		}
		return out
	}
	out := fragment{nullable: true}
	for _, child := range p.children {
		out = g.seq(out, g.expand(child))
	}
	return out
}

// expand builds p with its occurrence range: min copies, then either a loop
// or max-min optional copies.
func (g *glushkov) expand(p *particle) fragment {
	out := fragment{nullable: true}
	if p.max == 0 || g.tooMany {
		return out
	}
	for i := 0; i < p.min; i++ {
		out = g.seq(out, g.term(p))
	}
	if p.max < 0 {
		loop := g.term(p)
		g.link(loop.last, loop.first)
		loop.nullable = true
		return g.seq(out, loop)
	}
	// Optional copies nest, so each can only follow the one before it.
	optional := fragment{nullable: true}
	for i := p.max - p.min; i > 0 && !g.tooMany; i-- {
		copy := g.term(p)
		optional = g.seq(copy, optional)
		optional.nullable = true
	}
	return g.seq(out, optional)
}
