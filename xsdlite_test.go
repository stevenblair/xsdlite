package xsdlite

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func featureSchema(t testing.TB) *Schema {
	t.Helper()
	schema, err := Compile(os.DirFS("testdata"), "features.xsd")
	if err != nil {
		t.Fatalf("features.xsd: %v", err)
	}
	return schema
}

func featureDocument(t testing.TB) string {
	t.Helper()
	data, err := os.ReadFile("testdata/features.xml")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func validate(t testing.TB, schema *Schema, document string) Result {
	t.Helper()
	result, err := schema.Validate(strings.NewReader(document), Options{})
	if err != nil {
		t.Fatalf("not well-formed: %v", err)
	}
	return result
}

func TestFeatureDocumentIsValid(t *testing.T) {
	if result := validate(t, featureSchema(t), featureDocument(t)); !result.Valid() {
		t.Fatalf("features.xml should be valid:\n%v", result.Violations)
	}
}

// Each case makes one edit to the valid document and names the violation it
// must cause: its code, its line, and a part of its message.
func TestViolations(t *testing.T) {
	schema := featureSchema(t)
	valid := featureDocument(t)
	cases := []struct {
		name, old, new string
		code           Code
		line           int
		message        string
	}{
		// Content models.
		{"missing required element", `<title>Spring  catalogue</title>`, ``, CodeElement, 5, `unexpected child element "issued"; expected title`},
		{"elements out of order", "<status>published</status>\n  <format>v1</format>", "<format>v1</format>\n  <status>published</status>", CodeElement, 7, `unexpected child element "status"; expected one of note, category`},
		{"undeclared element", `<format>v1</format>`, `<format>v1</format><bogus/>`, CodeElement, 7, `unexpected child element "bogus"`},
		{"too many occurrences", `<note>Second note</note>`, `<note>Second note</note><note>3</note><note>4</note>`, CodeElement, 9, `unexpected child element "note"; expected category`},
		{"incomplete content", `<category>TOOLS</category>
    <price currency="GBP" valid="2024-03-12T09:00:00Z">12.50</price>`, `<category>TOOLS</category>`, CodeElement, 15, `unexpected child element "discount"; expected price`},
		{"content ends early", `<square layer="2"><label>Plot</label><side>4</side></square>`, `<square layer="2"><label>Plot</label></square>`, CodeContent, 36, `content is incomplete; expected side`},
		{"child of an empty element", `<dimension axis="z" mm="1.1E3"/>`, `<dimension axis="z" mm="1.1E3"><x/></dimension>`, CodeElement, 27, `no child elements are allowed here`},
		{"text in element-only content", `<name>Spade</name>`, `stray<name>Spade</name>`, CodeContent, 23, `text is not allowed here`},
		{"group reference", `<discount currency="GBP">1.25</discount>`, `<discount currency="GBP">1.25</discount><price currency="GBP">1</price>`, CodeElement, 16, `unexpected child element "price"`},
		{"all group: repeated", `<retries>3</retries>`, `<retries>3</retries><retries>4</retries>`, CodeElement, 39, `element "retries" may appear only once here`},
		{"all group: missing", `<locale>en</locale>`, ``, CodeContent, 38, `content is incomplete; expected locale`},
		{"all group: undeclared", `<locale>en</locale>`, `<locale>en</locale><zone/>`, CodeElement, 40, `unexpected child element "zone"; expected one of timeout, started`},
		{"restriction removes an element", `<square xsi:type="tUnlabelledSquare"><side>1e1</side>`, `<square xsi:type="tUnlabelledSquare"><label>x</label><side>1e1</side>`, CodeElement, 37, `unexpected child element "label"; expected side`},
		{"strict wildcard namespace", `<x:extension>`, `<t:category code="ZZ">late</t:category><x:extension>`, CodeElement, 50, `unexpected child element "category"`},
		// Attributes.
		{"missing required attribute", ` version="1.2.0"`, ``, CodeAttribute, 2, `missing required attribute "version"`},
		{"undeclared attribute", `<link sku="hmr-002"`, `<link bogus="1" sku="hmr-002"`, CodeAttribute, 47, `attribute "bogus" is not allowed`},
		{"fixed attribute", `unit="each"`, `unit="pair"`, CodeAttribute, 12, `attribute "unit" has value "pair"; expected fixed value "each"`},
		{"prohibited attribute", `<discount currency="GBP">`, `<discount currency="GBP" valid="2024-03-12T09:00:00Z">`, CodeAttribute, 16, `attribute "valid" is not allowed`},
		{"attribute removed by restriction", `<square xsi:type="tUnlabelledSquare">`, `<square xsi:type="tUnlabelledSquare" layer="1">`, CodeAttribute, 37, `attribute "layer" is not allowed`},
		// Simple types.
		{"pattern", `version="1.2.0"`, `version="1.2.x"`, CodeAttribute, 2, `does not match the pattern of type tVersion`},
		{"pattern with a class subtraction", `sku="hmr-001"`, `sku="ham-001"`, CodeAttribute, 12, `does not match the pattern of type tSku`},
		{"pattern with name escapes", `<tag>heavy_duty</tag>`, `<tag>9lives</tag>`, CodeValue, 17, `value "9lives" does not match the pattern of type tTag`},
		{"enumeration", `<status>published</status>`, `<status>archived</status>`, CodeValue, 6, `is not one of the values allowed for type tStatus`},
		{"length in octets", `checksum="0aF1B2c3"`, `checksum="0aF1"`, CodeAttribute, 2, `must have length 4 for type tChecksum`},
		{"maximum length", `<title>Spring  catalogue</title>`, `<title>` + strings.Repeat("x", 41) + `</title>`, CodeValue, 4, `is longer than the maximum length 40`},
		{"empty value", `<name>Spade</name>`, `<name></name>`, CodeValue, 24, `must not be empty for type tTitle`},
		{"union", `colour="#00ff7F"`, `colour="#00ff7"`, CodeAttribute, 21, `is not valid for type tColour`},
		{"list item", `sizes="16 20"`, `sizes="16 61"`, CodeAttribute, 20, `has the list item "61", which is above the maximum of type tSize`},
		{"list length", `sizes="16 20"`, `sizes="1 2 3 4 5"`, CodeAttribute, 20, `is longer than the maximum length 4`},
		{"built-in range", `revision="3"`, `revision="256"`, CodeAttribute, 2, `is above the maximum of xs:unsignedByte`},
		{"whole number", `<retries>3</retries>`, `<retries>3.0</retries>`, CodeValue, 39, `is not a whole number`},
		{"exclusive bound", `radius="2.5"`, `radius="0"`, CodeAttribute, 35, `is below the minimum of type tPositive`},
		{"fraction digits", `>12.50</price>`, `>12.505</price>`, CodeValue, 15, `has more than 2 fraction digits`},
		{"restricted simple content", `<discount currency="GBP">1.25</discount>`, `<discount currency="GBP">100</discount>`, CodeValue, 16, `is above the maximum`},
		{"date range", `<issued>2024-03-12</issued>`, `<issued>1999-12-31</issued>`, CodeValue, 5, `is below the minimum of type tIssued`},
		{"date lexical form", `<issued>2024-03-12</issued>`, `<issued>2024-02-30</issued>`, CodeValue, 5, `is not a valid date`},
		{"dateTime", `valid="2024-03-12T09:00:00Z"`, `valid="2024-03-12 09:00"`, CodeAttribute, 15, `is not a valid dateTime`},
		{"duration", `tolerance="PT0.5S"`, `tolerance="P0.5D"`, CodeAttribute, 18, `is not a valid duration`},
		{"boolean", `active="1"`, `active="yes"`, CodeAttribute, 12, `is not a valid boolean`},
		{"float", `mm="330"`, `mm="33O"`, CodeAttribute, 18, `is not a valid float`},
		{"language", `lang="en-GB"`, `lang="en_GB"`, CodeAttribute, 2, `is not a valid xs:language`},
		{"qualified name", `kind="t:tool"`, `kind="nope:tool"`, CodeAttribute, 12, `is not a valid qualified name`},
		{"base64", `<data>SGVsbG8gd29ybGQ=</data>`, `<data>SGVsbG8*d29ybGQ=</data>`, CodeValue, 44, `is not a valid base64Binary value`},
		{"fixed element value", `<format>v1</format>`, `<format>v2</format>`, CodeValue, 7, `does not equal the fixed value "v1"`},
		// Types and substitution.
		{"abstract element", `<circle radius="2.5" fill="green"/>`, `<shape/>`, CodeElement, 35, `unexpected child element "shape"; expected one of product, circle, square`},
		{"unknown xsi:type", `xsi:type="tUnlabelledSquare"`, `xsi:type="tNoSuchType"`, CodeType, 37, `is not a type the schema defines`},
		{"unrelated xsi:type", `xsi:type="tUnlabelledSquare"`, `xsi:type="tLink"`, CodeType, 37, `is not derived from tSquare`},
		{"xsi:nil with content", `<remark xsi:nil="true"/>`, `<remark xsi:nil="true">text</remark>`, CodeContent, 49, `an element with xsi:nil must be empty`},
		{"xsi:nil not allowed", `<format>v1</format>`, `<format xsi:nil="true"/>`, CodeType, 7, `xsi:nil is not allowed on element "format"`},
		// Identity constraints.
		{"duplicate key", `<category code="TOOLS" since="2019">`, `<category code="GDN" since="2019">`, CodeIdentity, 11, `duplicate value "GDN" for key "categoryKey"; it is first used on line 10`},
		{"dangling key reference from an element value", "<name>Spade</name>\n    <category>GDN</category>", "<name>Spade</name>\n    <category>TOYS</category>", CodeIdentity, 23, `value "TOYS" has no matching "categoryKey" key (key reference "productCategory")`},
		{"unique over a union of paths", `sku="trwl-011"/>`, `sku="hmr-001"/>`, CodeIdentity, 33, `duplicate value "hmr-001" for unique constraint "uniqueSku"`},
		{"unique on element text", `<tag>#bestseller</tag>`, `<tag>heavy_duty</tag>`, CodeIdentity, 19, `duplicate value "heavy_duty" for unique constraint "uniqueTag"`},
		{"two-field key reference to a key of a descendant", `<link sku="trwl-011" colour="blue"/>`, `<link sku="trwl-011" colour="red"/>`, CodeIdentity, 48, `value "trwl-011", "red" has no matching "variantKey" key`},
		{"duplicate ID", `<product id="p3"`, `<product id="p1"`, CodeIdentity, 29, `the ID "p1" is used more than once`},
		{"dangling IDREF", `replaces="p1"`, `replaces="p9"`, CodeIdentity, 23, `the IDREF "p9" does not match any ID`},
		{"dangling IDREFS item", `related="p1 p3"`, `related="p1 p4"`, CodeIdentity, 23, `the IDREF "p4" does not match any ID`},
	}
	for _, c := range cases {
		if !strings.Contains(valid, c.old) {
			t.Errorf("%s: the document has no %q", c.name, c.old)
			continue
		}
		result := validate(t, schema, strings.Replace(valid, c.old, c.new, 1))
		found := false
		for _, v := range result.Violations {
			if v.Code == c.code && v.Line == c.line && strings.Contains(v.Message, c.message) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want %s on line %d containing %q, got:\n%v", c.name, c.code, c.line, c.message, result.Violations)
		}
		if len(result.Violations) > 2 {
			t.Errorf("%s: one edit caused %d violations:\n%v", c.name, len(result.Violations), result.Violations)
		}
	}
}

func TestValuesCompareInTheirValueSpace(t *testing.T) {
	schema := featureSchema(t)
	valid := featureDocument(t)
	// Whitespace is collapsed, and a default supplies a missing attribute.
	for old, new := range map[string]string{
		`<category>GDN</category>`:           `<category>  GDN </category>`,
		`<status>published</status>`:         `<status/>`,
		`<format>v1</format>`:                `<format/>`,
		`>12.50</price>`:                     `> 12.5 </price>`,
		`mm="1.1E3"`:                         `mm="INF"`,
		`<started>08:30:00+01:00`:            `<started>24:00:00`,
		`<remark xsi:nil="true"/>`:           `<remark>plain</remark>`,
		`xsi:type="tUnlabelledSquare"`:       `xsi:type="t:tSquare"`,
		`<issued>2024-03-12</issued>`:        `<issued>2024-02-29+14:00</issued>`,
		`valid="2024-03-12T09:00:00Z"`:       `valid="-0044-03-15T12:00:00.25-05:00"`,
		`tolerance="PT0.5S"`:                 `tolerance="-P1Y2M3DT4H5M6.7S"`,
		`<data>SGVsbG8gd29ybGQ=</data>`:      `<data>SGVs bG8g d29y bGQ=</data>`,
		`<link sku="hmr-002" colour="red"/>`: `<link sku=" hmr-002 " colour="red"/>`,
	} {
		if !strings.Contains(valid, old) {
			t.Fatalf("the document has no %q", old)
		}
		if result := validate(t, schema, strings.Replace(valid, old, new, 1)); !result.Valid() {
			t.Errorf("%q should still be valid:\n%v", new, result.Violations)
		}
	}
}

func TestOneMistakeIsReportedOnce(t *testing.T) {
	// A product with no name: the misplaced children are still validated, so
	// the keys they hold are registered and nothing else is reported.
	doc := strings.Replace(featureDocument(t), "<name>Claw hammer</name>", "", 1)
	result := validate(t, featureSchema(t), doc)
	if len(result.Violations) != 1 || result.Violations[0].Path != "/catalog/product" || result.Violations[0].Line != 14 {
		t.Fatalf("got %v", result.Violations)
	}
}

func TestMaxViolations(t *testing.T) {
	doc := strings.ReplaceAll(featureDocument(t), `currency="`, `money="`)
	result, err := featureSchema(t).Validate(strings.NewReader(doc), Options{MaxViolations: 3})
	if err != nil || len(result.Violations) != 3 || !result.Truncated || result.Valid() {
		t.Fatalf("got %d violations, truncated %v, err %v", len(result.Violations), result.Truncated, err)
	}
}

func TestDocumentsThatAreNotWellFormed(t *testing.T) {
	schema := featureSchema(t)
	for name, doc := range map[string]string{
		"unclosed":  `<catalog xmlns="urn:xsdlite:test"><title>`,
		"mismatch":  `<catalog xmlns="urn:xsdlite:test"></catalogue>`,
		"empty":     ``,
		"two roots": `<a/><b/>`,
		"text":      `<a/>trailing`,
	} {
		_, err := schema.Validate(strings.NewReader(doc), Options{})
		var syntax *SyntaxError
		if !errors.As(err, &syntax) {
			t.Errorf("%s: want a SyntaxError, got %v", name, err)
		}
	}
	result := validate(t, schema, `<other/>`)
	if len(result.Violations) != 1 || result.Violations[0].Code != CodeRoot {
		t.Errorf("an undeclared root: %v", result.Violations)
	}
}

func TestOtherEncodings(t *testing.T) {
	doc := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n" + strings.Replace(strings.SplitN(featureDocument(t), "\n", 2)[1], "Spring  catalogue", "Fr\xfchling", 1)
	if result := validate(t, featureSchema(t), doc); !result.Valid() {
		t.Errorf("ISO-8859-1: %v", result.Violations)
	}
}

func TestConcurrentValidation(t *testing.T) {
	schema := featureSchema(t)
	doc := featureDocument(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if result, err := schema.Validate(strings.NewReader(doc), Options{}); err != nil || !result.Valid() {
					t.Errorf("concurrent run: %v %v", err, result.Violations)
				}
			}
		}()
	}
	wg.Wait()
}

func compileOne(schema string) (*Schema, error) {
	return Compile(fstest.MapFS{"s.xsd": {Data: []byte(`<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns="urn:x" targetNamespace="urn:x" elementFormDefault="qualified">` + schema + `</xs:schema>`)}}, "s.xsd")
}

func TestSchemasThatDoNotCompile(t *testing.T) {
	cases := []struct {
		name, schema, message string
		unsupported           bool
	}{
		{"unknown type", `<xs:element name="a" type="missing"/>`, "type {urn:x}missing is not defined", false},
		{"circular type", `<xs:simpleType name="a"><xs:restriction base="b"/></xs:simpleType><xs:simpleType name="b"><xs:restriction base="a"/></xs:simpleType>`, "defined in terms of itself", false},
		{"duplicate", `<xs:element name="a"/><xs:element name="a"/>`, "defined twice", false},
		{"ambiguous content", `<xs:complexType name="a"><xs:sequence><xs:element name="x" minOccurs="0"/><xs:element name="x"/></xs:sequence></xs:complexType>`, "is ambiguous", false},
		{"ambiguous wildcard", `<xs:complexType name="a"><xs:sequence><xs:element name="x" minOccurs="0"/><xs:any/></xs:sequence></xs:complexType>`, "is ambiguous", false},
		{"bad enumeration", `<xs:simpleType name="a"><xs:restriction base="xs:int"><xs:enumeration value="one"/></xs:restriction></xs:simpleType>`, `the enumeration value "one" is not a valid decimal number`, false},
		{"bad default", `<xs:complexType name="a"><xs:attribute name="n" type="xs:int" default="x"/></xs:complexType>`, "the default or fixed value", false},
		{"keyref without key", `<xs:element name="a"><xs:keyref name="r" refer="k"><xs:selector xpath="b"/><xs:field xpath="@c"/></xs:keyref></xs:element>`, "which is not a key", false},
		{"all with repeats", `<xs:complexType name="a"><xs:all><xs:element name="x" maxOccurs="2"/></xs:all></xs:complexType>`, "at most once", false},
		{"redefine", `<xs:redefine schemaLocation="other.xsd"/>`, "xs:redefine", true},
		{"xpath beyond the subset", `<xs:element name="a"><xs:key name="k"><xs:selector xpath="b[@c='1']"/><xs:field xpath="@c"/></xs:key></xs:element>`, "outside the subset", true},
		{"pattern Go cannot run", `<xs:simpleType name="a"><xs:restriction base="xs:string"><xs:pattern value="a{1,5000}"/></xs:restriction></xs:simpleType>`, "cannot be used", true},
		{"bounds on a type with no order", `<xs:simpleType name="a"><xs:restriction base="xs:gYear"><xs:minInclusive value="2000"/></xs:restriction></xs:simpleType>`, "not a number, date or time", true},
	}
	for _, c := range cases {
		_, err := compileOne(c.schema)
		var schemaErr *SchemaError
		if !errors.As(err, &schemaErr) || !strings.Contains(err.Error(), c.message) || schemaErr.Unsupported != c.unsupported {
			t.Errorf("%s: want a SchemaError containing %q (unsupported %v), got %v", c.name, c.message, c.unsupported, err)
		}
	}
	if _, err := Compile(fstest.MapFS{}, "missing.xsd"); err == nil {
		t.Error("a missing schema file compiled")
	}
}

func TestImportsAndUnqualifiedNames(t *testing.T) {
	files := fstest.MapFS{
		"main.xsd": {Data: []byte(`<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:p="urn:parts" targetNamespace="urn:main" xmlns="urn:main">
			<xs:import namespace="urn:parts" schemaLocation="sub/parts.xsd"/>
			<xs:import namespace="http://www.w3.org/XML/1998/namespace"/>
			<xs:element name="box">
				<xs:complexType>
					<xs:sequence>
						<xs:element name="local" type="xs:int"/>
						<xs:element ref="p:part" maxOccurs="unbounded"/>
					</xs:sequence>
					<xs:attribute ref="xml:lang"/>
					<xs:attribute ref="p:grade" use="required"/>
				</xs:complexType>
			</xs:element>
		</xs:schema>`)},
		"sub/parts.xsd": {Data: []byte(`<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="urn:parts" xmlns="urn:parts" elementFormDefault="qualified">
			<xs:include schemaLocation="chameleon.xsd"/>
			<xs:element name="part" type="size"/>
			<xs:attribute name="grade" type="size"/>
		</xs:schema>`)},
		"sub/chameleon.xsd": {Data: []byte(`<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
			<xs:simpleType name="size"><xs:restriction base="xs:token"><xs:enumeration value="S"/><xs:enumeration value="L"/></xs:restriction></xs:simpleType>
		</xs:schema>`)},
	}
	schema, err := Compile(files, "main.xsd")
	if err != nil {
		t.Fatal(err)
	}
	good := `<m:box xmlns:m="urn:main" xmlns:p="urn:parts" xml:lang="en" p:grade="S"><local>1</local><p:part>L</p:part></m:box>`
	if result := validate(t, schema, good); !result.Valid() {
		t.Errorf("valid document: %v", result.Violations)
	}
	for bad, message := range map[string]string{
		strings.Replace(good, `<local>1</local>`, `<m:local>1</m:local>`, 1): `unexpected child element "local"`,
		strings.Replace(good, `<p:part>L</p:part>`, `<p:part>M</p:part>`, 1): `is not one of the values allowed`,
		strings.Replace(good, ` p:grade="S"`, ` grade="S"`, 1):               `attribute "grade" is not allowed`,
		strings.Replace(good, `xml:lang="en"`, `xml:lang="e n"`, 1):          `is not a valid xs:language`,
	} {
		result := validate(t, schema, bad)
		if len(result.Violations) == 0 || !strings.Contains(result.Violations[0].Message, message) {
			t.Errorf("want %q, got %v", message, result.Violations)
		}
	}
}

func TestPatterns(t *testing.T) {
	cases := []struct {
		pattern string
		yes, no []string
	}{
		{`a.c`, []string{"abc", "a c"}, []string{"ac", "abcd", "a\nc"}},
		{`^a$`, []string{"^a$"}, []string{"a"}},
		{`\d+`, []string{"123", "٣"}, []string{"", "1a"}},
		{`\w+`, []string{"abc9", "é"}, []string{"a_b", "a b", "a-b"}},
		{`\W`, []string{"_", " ", "-"}, []string{"a"}},
		{`\s\S`, []string{" a", "\ta"}, []string{"a ", "  "}},
		{`\i\c*`, []string{"a", "_a.b-c", ":x9"}, []string{"9a", "-a", ""}},
		{`[\i-[:]][\c-[:]]*`, []string{"abc", "_a-1"}, []string{"a:b", ":a"}},
		{`[a-z-[aeiou]]+`, []string{"bcd"}, []string{"abc", "B"}},
		{`[^\d]`, []string{"a"}, []string{"1"}},
		{`[\w-[\d]]+`, []string{"abc"}, []string{"ab1"}},
		{`[+-]?\d`, []string{"+1", "-1", "1"}, []string{"*1"}},
		{`\p{Lu}\p{Ll}*`, []string{"Abc", "É"}, []string{"abc", "A1"}},
		{`\P{L}`, []string{"1", "-"}, []string{"a"}},
		{`[\p{IsBasicLatin}\p{IsLatin-1Supplement}]*`, []string{"", "aé~"}, []string{"Ā", "€"}},
		{`\p{IsGreek}+`, []string{"αβγ"}, []string{"abc"}},
		{`\P{IsBasicLatin}`, []string{"é"}, []string{"e"}},
		{`(ab|cd){2,3}`, []string{"abcd", "ababab"}, []string{"ab", "abababab"}},
		{`a\.b\-c\\`, []string{`a.b-c\`}, []string{`aXb-c\`}},
		{`[\-\]\[\^]+`, []string{"-][^"}, []string{"a"}},
		{`x{2}`, []string{"xx"}, []string{"x", "xxx"}},
	}
	for _, c := range cases {
		re, err := compilePattern(c.pattern)
		if err != nil {
			t.Errorf("%s: %v", c.pattern, err)
			continue
		}
		for _, s := range c.yes {
			if !re.MatchString(s) {
				t.Errorf("%s should match %q (as %s)", c.pattern, s, re)
			}
		}
		for _, s := range c.no {
			if re.MatchString(s) {
				t.Errorf("%s should not match %q (as %s)", c.pattern, s, re)
			}
		}
	}
	for _, bad := range []string{`[a-`, `\q`, `\p{IsNowhere}`, `\p{Xx}`, `a\`, `[z-a]`} {
		if _, err := compilePattern(bad); err == nil {
			t.Errorf("%s compiled", bad)
		}
	}
}

func TestBuiltInTypes(t *testing.T) {
	c := newCompiler(fstest.MapFS{})
	c.builtins()
	cases := []struct {
		typ     string
		yes, no []string
	}{
		{"boolean", []string{"true", "false", "1", "0", " true "}, []string{"TRUE", "yes", ""}},
		{"decimal", []string{"1", "-1.5", "+.5", "1.", "007"}, []string{"", ".", "1e3", "1,5", "--1"}},
		{"integer", []string{"0", "-12", "+7"}, []string{"1.0", "1.", "a"}},
		{"long", []string{"9223372036854775807", "-9223372036854775808"}, []string{"9223372036854775808"}},
		{"int", []string{"2147483647"}, []string{"2147483648", "-2147483649"}},
		{"short", []string{"-32768"}, []string{"32768"}},
		{"byte", []string{"127", "-128"}, []string{"128"}},
		{"unsignedLong", []string{"18446744073709551615"}, []string{"18446744073709551616", "-1"}},
		{"unsignedInt", []string{"4294967295"}, []string{"4294967296"}},
		{"unsignedShort", []string{"65535"}, []string{"65536"}},
		{"positiveInteger", []string{"1"}, []string{"0"}},
		{"negativeInteger", []string{"-1"}, []string{"0"}},
		{"nonPositiveInteger", []string{"0", "-0"}, []string{"1"}},
		{"float", []string{"1", "-1.5E-3", "INF", "-INF", "NaN", "1e400"}, []string{"inf", "+INF", "1e", "0x1p3", "1_0"}},
		{"double", []string{"1.7976931348623157E308"}, []string{"1.0f"}},
		{"dateTime", []string{"2024-02-29T23:59:59", "2024-01-01T24:00:00", "12024-01-01T00:00:00.123+14:00"}, []string{"2023-02-29T00:00:00", "2024-01-01T24:00:01", "2024-1-1T00:00:00", "0000-01-01T00:00:00", "2024-01-01T00:00:00+15:00", "2024-01-01"}},
		{"date", []string{"2024-12-31", "2024-12-31Z", "-0001-01-01"}, []string{"2024-13-01", "24-12-31", "2024-12-31T00:00:00"}},
		{"time", []string{"00:00:00", "23:59:59.999-05:30"}, []string{"24:01:00", "1:00:00", "12:60:00"}},
		{"gYearMonth", []string{"2024-02"}, []string{"2024-13", "2024"}},
		{"gYear", []string{"2024", "-0044Z"}, []string{"24", "02024"}},
		{"gMonthDay", []string{"--02-29"}, []string{"--02-30", "02-29"}},
		{"gDay", []string{"---31"}, []string{"---32", "--31"}},
		{"gMonth", []string{"--12"}, []string{"--13", "12"}},
		{"duration", []string{"P1Y", "PT1S", "-P1M", "P1Y2M3DT4H5M6.5S", "PT0.001S"}, []string{"P", "PT", "1Y", "P1S", "P1YT", "PT1.5M", "P1M1Y"}},
		{"hexBinary", []string{"", "0aF1"}, []string{"0a1", "0g"}},
		{"base64Binary", []string{"", "QQ==", "QUJD"}, []string{"Q", "QQ=", "====", "QQ=Q"}},
		{"anyURI", []string{"", "http://example.com/a?b#c", "a/b:c", "urn:x:y", "%20"}, []string{"1a:b", ":b", "%2", "%zz"}},
		{"language", []string{"en", "en-GB", "x-klingon1"}, []string{"", "english12", "e_n"}},
		{"Name", []string{"a", "_a", "a:b", "é"}, []string{"1a", "a b", ""}},
		{"NCName", []string{"a.b-c"}, []string{"a:b", ".a"}},
		{"NMTOKEN", []string{"1a", "-.:"}, []string{"a b", ""}},
		{"NMTOKENS", []string{"a b  c"}, []string{"", "a,b"}},
		{"QName", []string{"a", "xml:lang"}, []string{"a:b:c", ":a", "1:a"}},
		{"token", []string{"", " a  b "}, nil},
	}
	for _, tc := range cases {
		st := c.s.types[xname{xsdNS, tc.typ}].simple
		for _, v := range tc.yes {
			if _, _, reason := st.validate(v, nil); reason != "" {
				t.Errorf("xs:%s should accept %q: %s", tc.typ, v, reason)
			}
		}
		for _, v := range tc.no {
			if _, _, reason := st.validate(v, nil); reason == "" {
				t.Errorf("xs:%s should reject %q", tc.typ, v)
			}
		}
	}
	// Equal values have equal canonical forms.
	equal := map[string][2]string{
		"decimal":   {"+01.50", "1.5"},
		"boolean":   {"1", "true"},
		"double":    {"1e2", "100"},
		"dateTime":  {"2024-01-01T12:00:00+01:00", "2024-01-01T11:00:00Z"},
		"hexBinary": {"0a", "0A"},
		"token":     {" a  b ", "a b"},
	}
	for typ, pair := range equal {
		st := c.s.types[xname{xsdNS, typ}].simple
		a, _, _ := st.validate(pair[0], nil)
		b, _, _ := st.validate(pair[1], nil)
		if a != b {
			t.Errorf("xs:%s: %q and %q should be equal, got %q and %q", typ, pair[0], pair[1], a, b)
		}
	}
}
