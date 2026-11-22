// Command difftest compares xsdlite with an independent XML Schema validator,
// github.com/jacoelho/xsd, on documents derived from valid ones by small
// random edits. The two should agree on which documents are valid.
//
//	go run . [-n 2000] [-seed 1] [-v] schema.xsd document.xml [more.xml ...]
//
// It lives in its own module so that xsdlite itself has no dependencies.
package main

import (
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/jacoelho/xsd"
	"github.com/jacoelho/xsd/xsderrors"
	"github.com/stevenblair/xsdlite"
)

type element struct {
	name     xml.Name
	attrs    []xml.Attr
	children []any // *element or string
	parent   *element
}

func parse(data []byte) (*element, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var root, cur *element
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			return root, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			e := &element{name: t.Name, attrs: append([]xml.Attr(nil), t.Attr...), parent: cur}
			if cur == nil {
				root = e
			} else {
				cur.children = append(cur.children, e)
			}
			cur = e
		case xml.EndElement:
			cur = cur.parent
		case xml.CharData:
			if cur != nil {
				cur.children = append(cur.children, string(t))
			}
		}
	}
}

func qualified(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

func (e *element) write(b *bytes.Buffer) {
	b.WriteString("<" + qualified(e.name))
	for _, a := range e.attrs {
		b.WriteString(" " + qualified(a.Name) + `="`)
		xml.EscapeText(b, []byte(a.Value))
		b.WriteString(`"`)
	}
	if len(e.children) == 0 {
		b.WriteString("/>")
		return
	}
	b.WriteString(">")
	for _, c := range e.children {
		switch c := c.(type) {
		case *element:
			c.write(b)
		case string:
			xml.EscapeText(b, []byte(c))
		}
	}
	b.WriteString("</" + qualified(e.name) + ">")
}

func (e *element) clone(parent *element) *element {
	c := &element{name: e.name, attrs: append([]xml.Attr(nil), e.attrs...), parent: parent}
	for _, child := range e.children {
		if el, ok := child.(*element); ok {
			c.children = append(c.children, el.clone(c))
		} else {
			c.children = append(c.children, child)
		}
	}
	return c
}

func all(e *element, into *[]*element) {
	*into = append(*into, e)
	for _, c := range e.children {
		if el, ok := c.(*element); ok {
			all(el, into)
		}
	}
}

func (e *element) index() int {
	for i, c := range e.parent.children {
		if c == any(e) {
			return i
		}
	}
	return -1
}

func isNamespace(a xml.Attr) bool { return a.Name.Space == "xmlns" || a.Name.Local == "xmlns" }

var values = []string{"", " ", "0", "1", "-1", "1.5", "true", "x", "x y", "ZZZ", "None", "0A", "65536", "01-0C-CD-01-00-01", "a/b", "LLN0", "é", "0000", "2007", "unbounded", "ST", "A1"}

// mutate applies one random edit to a copy of root and describes it.
func mutate(root *element, r *rand.Rand, texts []string) (*element, string) {
	doc := root.clone(nil)
	var nodes []*element
	all(doc, &nodes)
	for tries := 0; tries < 50; tries++ {
		e := nodes[r.Intn(len(nodes))]
		path := qualified(e.name)
		for p := e.parent; p != nil; p = p.parent {
			path = qualified(p.name) + "/" + path
		}
		var plain []int
		for i, a := range e.attrs {
			if !isNamespace(a) {
				plain = append(plain, i)
			}
		}
		switch r.Intn(11) {
		case 0:
			if len(plain) == 0 {
				continue
			}
			i := plain[r.Intn(len(plain))]
			name := qualified(e.attrs[i].Name)
			e.attrs = append(e.attrs[:i], e.attrs[i+1:]...)
			return doc, "delete attribute " + name + " of " + path
		case 1, 2:
			if len(plain) == 0 {
				continue
			}
			i := plain[r.Intn(len(plain))]
			value := values[r.Intn(len(values))]
			if r.Intn(3) == 0 {
				value = texts[r.Intn(len(texts))]
			}
			old := e.attrs[i].Value
			e.attrs[i].Value = value
			return doc, fmt.Sprintf("set attribute %s of %s from %q to %q", qualified(e.attrs[i].Name), path, old, value)
		case 3:
			if e.parent == nil {
				continue
			}
			i := e.index()
			e.parent.children = append(e.parent.children[:i], e.parent.children[i+1:]...)
			return doc, "delete element " + path
		case 4:
			if e.parent == nil {
				continue
			}
			i := e.index()
			dup := e.clone(e.parent)
			rest := append([]any{dup}, e.parent.children[i+1:]...)
			e.parent.children = append(e.parent.children[:i+1], rest...)
			return doc, "duplicate element " + path
		case 5:
			var kids []int
			for i, c := range e.children {
				if _, ok := c.(*element); ok {
					kids = append(kids, i)
				}
			}
			if len(kids) < 2 {
				continue
			}
			a := r.Intn(len(kids) - 1)
			i, j := kids[a], kids[a+1]
			if qualified(e.children[i].(*element).name) == qualified(e.children[j].(*element).name) {
				continue
			}
			e.children[i], e.children[j] = e.children[j], e.children[i]
			return doc, fmt.Sprintf("swap children %d and %d of %s", a, a+1, path)
		case 6:
			if e.parent == nil {
				continue
			}
			other := nodes[r.Intn(len(nodes))]
			old := qualified(e.name)
			if qualified(other.name) == old {
				e.name.Local = "Bogus"
			} else {
				e.name = other.name
			}
			return doc, "rename element " + path + " to " + qualified(e.name)
		case 7:
			e.children = append(e.children, "stray text")
			return doc, "add text to " + path
		case 8:
			name := []string{"bogus", "name", "type", "desc", "id", "uuid", "inst", "value"}[r.Intn(8)]
			exists := false
			for _, a := range e.attrs {
				exists = exists || a.Name.Local == name
			}
			if exists {
				continue
			}
			value := values[r.Intn(len(values))]
			e.attrs = append(e.attrs, xml.Attr{Name: xml.Name{Local: name}, Value: value})
			return doc, fmt.Sprintf("add attribute %s=%q to %s", name, value, path)
		case 9:
			for i, c := range e.children {
				if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
					value := values[r.Intn(len(values))]
					e.children[i] = value
					return doc, fmt.Sprintf("set text of %s from %q to %q", path, s, value)
				}
			}
		case 10:
			other := nodes[r.Intn(len(nodes))]
			if other == e || other.parent == nil {
				continue
			}
			e.children = append(e.children, other.clone(e))
			return doc, "copy element " + qualified(other.name) + " into " + path
		}
	}
	return doc, "no change"
}

func main() {
	count := flag.Int("n", 2000, "mutants per document")
	seed := flag.Int64("seed", 1, "random seed")
	verbose := flag.Bool("v", false, "print every disagreement in full")
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: difftest [-n N] [-seed S] schema.xsd document.xml ...")
		os.Exit(2)
	}
	schemaPath := flag.Arg(0)
	lite, err := xsdlite.Compile(os.DirFS(filepath.Dir(schemaPath)), filepath.Base(schemaPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "xsdlite:", err)
		os.Exit(2)
	}
	// The reference is given every schema file of the directory, root first.
	names, _ := filepath.Glob(filepath.Join(filepath.Dir(schemaPath), "*.xsd"))
	sources := []xsd.SchemaSource{}
	for _, name := range append([]string{schemaPath}, names...) {
		if name == schemaPath && len(sources) > 0 {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		sources = append(sources, xsd.Bytes(filepath.Base(name), data))
	}
	reference, err := xsd.Compile(sources...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reference:", err)
		os.Exit(2)
	}
	total, invalid, disagreements := 0, 0, 0
	for _, name := range flag.Args()[1:] {
		data, err := os.ReadFile(name)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		root, err := parse(data)
		if err != nil {
			fmt.Fprintln(os.Stderr, name, err)
			os.Exit(2)
		}
		var nodes []*element
		all(root, &nodes)
		var texts []string
		for _, e := range nodes {
			for _, a := range e.attrs {
				if !isNamespace(a) {
					texts = append(texts, a.Value)
				}
			}
		}
		r := rand.New(rand.NewSource(*seed))
		for i := 0; i <= *count; i++ {
			doc, what := root, "unchanged"
			if i > 0 {
				doc, what = mutate(root, r, texts)
			}
			var buf bytes.Buffer
			doc.write(&buf)
			refErr := reference.Validate(bytes.NewReader(buf.Bytes()))
			result, liteErr := lite.Validate(bytes.NewReader(buf.Bytes()), xsdlite.Options{})
			refValid := refErr == nil
			liteValid := liteErr == nil && result.Valid()
			total++
			if !refValid {
				invalid++
			}
			if refValid == liteValid {
				continue
			}
			disagreements++
			if disagreements <= 40 || *verbose {
				fmt.Printf("\nDISAGREE %s #%d: %s\n", filepath.Base(name), i, what)
				if refErr != nil {
					for j, e := range xsderrors.Flatten(refErr) {
						if j < 3 {
							fmt.Printf("  reference: %.300s\n", e.Error())
						}
					}
				} else {
					fmt.Println("  reference: valid")
				}
				if liteErr != nil {
					fmt.Printf("  xsdlite:   error %v\n", liteErr)
				} else if liteValid {
					fmt.Println("  xsdlite:   valid")
				}
				for j, v := range result.Violations {
					if j < 3 {
						fmt.Printf("  xsdlite:   %s\n", v)
					}
				}
			}
		}
	}
	fmt.Printf("\n%d documents, %d invalid by the reference, %d disagreements\n", total, invalid, disagreements)
	if disagreements > 0 {
		os.Exit(1)
	}
}
