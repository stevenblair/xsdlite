package xsdlite

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// XML Schema patterns are close to Go's regular expressions but not the same:
// they match the whole value, ^ and $ are ordinary characters, \d and \w are
// Unicode-wide, \i and \c name XML name characters, \p{IsBlock} names Unicode
// blocks, and a character class can subtract another. compilePattern
// translates a pattern into an equivalent Go expression.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	t := &patternTranslator{src: []rune(pattern)}
	if err := t.branch(); err != nil {
		return nil, err
	}
	return regexp.Compile("^(?:" + t.out.String() + ")$")
}

type patternTranslator struct {
	src []rune
	i   int
	out strings.Builder
}

func (t *patternTranslator) branch() error {
	for t.i < len(t.src) {
		c := t.src[t.i]
		switch c {
		case '\\':
			set, err := t.escape()
			if err != nil {
				return err
			}
			t.out.WriteString(set.class())
		case '[':
			t.i++
			set, err := t.class()
			if err != nil {
				return err
			}
			t.out.WriteString(set.class())
		case '.':
			t.i++
			t.out.WriteString(`[^\n\r]`)
		case '^', '$':
			t.i++
			t.out.WriteString(`\` + string(c))
		default:
			t.i++
			t.out.WriteRune(c)
		}
	}
	return nil
}

// charSet is a character class: either text Go understands inside brackets,
// or, when the class needs set arithmetic, explicit sorted rune ranges.
type charSet struct {
	negated bool
	text    string // contents of a Go bracket expression
	ranges  []rune // inclusive pairs; used when exact is true
	exact   bool
}

func (s charSet) class() string {
	if s.exact {
		return "[" + rangesText(s.ranges) + "]"
	}
	if s.negated {
		return "[^" + s.text + "]"
	}
	return "[" + s.text + "]"
}

func rangesText(ranges []rune) string {
	if len(ranges) == 0 {
		return `^\x{0}-\x{10FFFF}`
	}
	var b strings.Builder
	for i := 0; i < len(ranges); i += 2 {
		fmt.Fprintf(&b, `\x{%X}`, ranges[i])
		if ranges[i+1] != ranges[i] {
			fmt.Fprintf(&b, `-\x{%X}`, ranges[i+1])
		}
	}
	return b.String()
}

func literalSet(lo, hi rune) charSet {
	return charSet{text: rangesText([]rune{lo, hi}), ranges: []rune{lo, hi}}
}

// escape reads a backslash escape at t.i and returns it as a set.
func (t *patternTranslator) escape() (charSet, error) {
	t.i++
	if t.i >= len(t.src) {
		return charSet{}, fmt.Errorf("it ends with a backslash")
	}
	c := t.src[t.i]
	t.i++
	switch c {
	case 'n':
		return literalSet('\n', '\n'), nil
	case 'r':
		return literalSet('\r', '\r'), nil
	case 't':
		return literalSet('\t', '\t'), nil
	case '\\', '|', '.', '-', '^', '?', '*', '+', '{', '}', '(', ')', '[', ']':
		return literalSet(c, c), nil
	case 's', 'S':
		return charSet{negated: c == 'S', text: ` \t\n\r`, ranges: []rune{'\t', '\n', '\r', '\r', ' ', ' '}}, nil
	case 'd', 'D':
		return charSet{negated: c == 'D', text: `\p{Nd}`, ranges: tableRanges(unicode.Nd)}, nil
	case 'w', 'W':
		not := mergeRanges(append(append(tableRanges(unicode.P), tableRanges(unicode.Z)...), tableRanges(unicode.C)...))
		return charSet{negated: c == 'w', text: `\p{P}\p{Z}\p{C}`, ranges: not}, nil
	case 'i', 'I':
		return charSet{negated: c == 'I', text: rangesText(nameStartRanges), ranges: mergeRanges(nameStartRanges)}, nil
	case 'c', 'C':
		all := mergeRanges(append(append([]rune{}, nameStartRanges...), nameExtraRanges...))
		return charSet{negated: c == 'C', text: rangesText(all), ranges: all}, nil
	case 'p', 'P':
		if t.i >= len(t.src) || t.src[t.i] != '{' {
			return charSet{}, fmt.Errorf(`\%c is not followed by {name}`, c)
		}
		end := t.i
		for end < len(t.src) && t.src[end] != '}' {
			end++
		}
		if end >= len(t.src) {
			return charSet{}, fmt.Errorf(`\%c{ is not closed`, c)
		}
		name := string(t.src[t.i+1 : end])
		t.i = end + 1
		if block, ok := strings.CutPrefix(name, "Is"); ok {
			ranges, ok := unicodeBlocks[block]
			if !ok {
				return charSet{}, fmt.Errorf("the Unicode block %q is not known", block)
			}
			return charSet{negated: c == 'P', text: rangesText(ranges), ranges: ranges}, nil
		}
		table, ok := unicode.Categories[name]
		if !ok || len(name) > 2 {
			return charSet{}, fmt.Errorf("the character category %q is not known", name)
		}
		return charSet{negated: c == 'P', text: `\p{` + name + `}`, ranges: tableRanges(table)}, nil
	}
	return charSet{}, fmt.Errorf(`\%c is not an escape`, c)
}

// class reads a bracket expression after its opening bracket.
func (t *patternTranslator) class() (charSet, error) {
	negated := false
	if t.i < len(t.src) && t.src[t.i] == '^' {
		negated = true
		t.i++
	}
	var parts []charSet
	var subtract *charSet
	exact := false
	closed := false
	for t.i < len(t.src) && !closed {
		c := t.src[t.i]
		switch {
		case c == ']' && len(parts) > 0:
			t.i++
			closed = true
		case c == '-' && t.i+1 < len(t.src) && t.src[t.i+1] == '[' && len(parts) > 0:
			t.i += 2
			inner, err := t.class()
			if err != nil {
				return charSet{}, err
			}
			subtract, exact = &inner, true
			if t.i >= len(t.src) || t.src[t.i] != ']' {
				return charSet{}, fmt.Errorf("a character class subtraction must end the class")
			}
		case c == '[':
			return charSet{}, fmt.Errorf("a character class contains an unescaped [")
		default:
			var part charSet
			if c == '\\' {
				var err error
				if part, err = t.escape(); err != nil {
					return charSet{}, err
				}
			} else {
				t.i++
				part = literalSet(c, c)
			}
			// A range: a single character, a hyphen, a single character.
			single := len(part.ranges) == 2 && part.ranges[0] == part.ranges[1] && !part.negated
			if single && t.i+1 < len(t.src) && t.src[t.i] == '-' && t.src[t.i+1] != ']' && t.src[t.i+1] != '[' {
				t.i++
				var high charSet
				if t.src[t.i] == '\\' {
					var err error
					if high, err = t.escape(); err != nil {
						return charSet{}, err
					}
				} else {
					high = literalSet(t.src[t.i], t.src[t.i])
					t.i++
				}
				if len(high.ranges) != 2 || high.ranges[0] != high.ranges[1] || high.negated || high.ranges[0] < part.ranges[0] {
					return charSet{}, fmt.Errorf("a character range is not valid")
				}
				part = literalSet(part.ranges[0], high.ranges[0])
			}
			if part.negated {
				// Go cannot nest a negated set inside a class.
				exact = true
			}
			parts = append(parts, part)
		}
	}
	if !closed {
		return charSet{}, fmt.Errorf("a character class is not closed")
	}
	if !exact {
		var text strings.Builder
		for _, part := range parts {
			text.WriteString(part.text)
		}
		var all []rune
		for _, part := range parts {
			all = append(all, part.ranges...)
		}
		return charSet{negated: negated, text: text.String(), ranges: mergeRanges(all)}, nil
	}
	var all []rune
	for _, part := range parts {
		all = append(all, part.exactRanges()...)
	}
	all = mergeRanges(all)
	if negated {
		all = invertRanges(all)
	}
	if subtract != nil {
		all = invertRanges(mergeRanges(append(invertRanges(all), subtract.exactRanges()...)))
	}
	return charSet{ranges: all, exact: true}, nil
}

func (s charSet) exactRanges() []rune {
	ranges := mergeRanges(s.ranges)
	if s.negated && !s.exact {
		return invertRanges(ranges)
	}
	return ranges
}

func tableRanges(table *unicode.RangeTable) []rune {
	var out []rune
	add := func(lo, hi, stride rune) {
		if stride == 1 {
			out = append(out, lo, hi)
			return
		}
		for r := lo; r <= hi; r += stride {
			out = append(out, r, r)
		}
	}
	for _, r := range table.R16 {
		add(rune(r.Lo), rune(r.Hi), rune(r.Stride))
	}
	for _, r := range table.R32 {
		add(rune(r.Lo), rune(r.Hi), rune(r.Stride))
	}
	return mergeRanges(out)
}

// mergeRanges sorts inclusive rune pairs and joins those that touch.
func mergeRanges(ranges []rune) []rune {
	if len(ranges) <= 2 {
		return ranges
	}
	pairs := make([][2]rune, 0, len(ranges)/2)
	for i := 0; i < len(ranges); i += 2 {
		pairs = append(pairs, [2]rune{ranges[i], ranges[i+1]})
	}
	// Insertion sort: the inputs are short or already nearly sorted.
	for i := 1; i < len(pairs); i++ {
		for j := i; j > 0 && pairs[j][0] < pairs[j-1][0]; j-- {
			pairs[j], pairs[j-1] = pairs[j-1], pairs[j]
		}
	}
	out := make([]rune, 0, len(ranges))
	for _, p := range pairs {
		if n := len(out); n > 0 && p[0] <= out[n-1]+1 {
			if p[1] > out[n-1] {
				out[n-1] = p[1]
			}
			continue
		}
		out = append(out, p[0], p[1])
	}
	return out
}

func invertRanges(ranges []rune) []rune {
	var out []rune
	next := rune(0)
	for i := 0; i < len(ranges); i += 2 {
		if ranges[i] > next {
			out = append(out, next, ranges[i]-1)
		}
		next = ranges[i+1] + 1
	}
	if next <= unicode.MaxRune {
		out = append(out, next, unicode.MaxRune)
	}
	return out
}

// unicodeBlocks are the block names XML Schema 1.0 defines for \p{IsName}.
var unicodeBlocks = map[string][]rune{
	"BasicLatin": {0x0000, 0x007F}, "Latin-1Supplement": {0x0080, 0x00FF}, "LatinExtended-A": {0x0100, 0x017F},
	"LatinExtended-B": {0x0180, 0x024F}, "IPAExtensions": {0x0250, 0x02AF}, "SpacingModifierLetters": {0x02B0, 0x02FF},
	"CombiningDiacriticalMarks": {0x0300, 0x036F}, "Greek": {0x0370, 0x03FF}, "Cyrillic": {0x0400, 0x04FF},
	"Armenian": {0x0530, 0x058F}, "Hebrew": {0x0590, 0x05FF}, "Arabic": {0x0600, 0x06FF}, "Syriac": {0x0700, 0x074F},
	"Thaana": {0x0780, 0x07BF}, "Devanagari": {0x0900, 0x097F}, "Bengali": {0x0980, 0x09FF}, "Gurmukhi": {0x0A00, 0x0A7F},
	"Gujarati": {0x0A80, 0x0AFF}, "Oriya": {0x0B00, 0x0B7F}, "Tamil": {0x0B80, 0x0BFF}, "Telugu": {0x0C00, 0x0C7F},
	"Kannada": {0x0C80, 0x0CFF}, "Malayalam": {0x0D00, 0x0D7F}, "Sinhala": {0x0D80, 0x0DFF}, "Thai": {0x0E00, 0x0E7F},
	"Lao": {0x0E80, 0x0EFF}, "Tibetan": {0x0F00, 0x0FFF}, "Myanmar": {0x1000, 0x109F}, "Georgian": {0x10A0, 0x10FF},
	"HangulJamo": {0x1100, 0x11FF}, "Ethiopic": {0x1200, 0x137F}, "Cherokee": {0x13A0, 0x13FF},
	"UnifiedCanadianAboriginalSyllabics": {0x1400, 0x167F}, "Ogham": {0x1680, 0x169F}, "Runic": {0x16A0, 0x16FF},
	"Khmer": {0x1780, 0x17FF}, "Mongolian": {0x1800, 0x18AF}, "LatinExtendedAdditional": {0x1E00, 0x1EFF},
	"GreekExtended": {0x1F00, 0x1FFF}, "GeneralPunctuation": {0x2000, 0x206F}, "SuperscriptsandSubscripts": {0x2070, 0x209F},
	"CurrencySymbols": {0x20A0, 0x20CF}, "CombiningMarksforSymbols": {0x20D0, 0x20FF}, "LetterlikeSymbols": {0x2100, 0x214F},
	"NumberForms": {0x2150, 0x218F}, "Arrows": {0x2190, 0x21FF}, "MathematicalOperators": {0x2200, 0x22FF},
	"MiscellaneousTechnical": {0x2300, 0x23FF}, "ControlPictures": {0x2400, 0x243F},
	"OpticalCharacterRecognition": {0x2440, 0x245F}, "EnclosedAlphanumerics": {0x2460, 0x24FF}, "BoxDrawing": {0x2500, 0x257F},
	"BlockElements": {0x2580, 0x259F}, "GeometricShapes": {0x25A0, 0x25FF}, "MiscellaneousSymbols": {0x2600, 0x26FF},
	"Dingbats": {0x2700, 0x27BF}, "BraillePatterns": {0x2800, 0x28FF}, "CJKRadicalsSupplement": {0x2E80, 0x2EFF},
	"KangxiRadicals": {0x2F00, 0x2FDF}, "IdeographicDescriptionCharacters": {0x2FF0, 0x2FFF},
	"CJKSymbolsandPunctuation": {0x3000, 0x303F}, "Hiragana": {0x3040, 0x309F}, "Katakana": {0x30A0, 0x30FF},
	"Bopomofo": {0x3100, 0x312F}, "HangulCompatibilityJamo": {0x3130, 0x318F}, "Kanbun": {0x3190, 0x319F},
	"BopomofoExtended": {0x31A0, 0x31BF}, "EnclosedCJKLettersandMonths": {0x3200, 0x32FF}, "CJKCompatibility": {0x3300, 0x33FF},
	"CJKUnifiedIdeographsExtensionA": {0x3400, 0x4DB5}, "CJKUnifiedIdeographs": {0x4E00, 0x9FFF},
	"YiSyllables": {0xA000, 0xA48F}, "YiRadicals": {0xA490, 0xA4CF}, "HangulSyllables": {0xAC00, 0xD7A3},
	"PrivateUse": {0xE000, 0xF8FF, 0xF0000, 0xFFFFD, 0x100000, 0x10FFFD}, "CJKCompatibilityIdeographs": {0xF900, 0xFAFF},
	"AlphabeticPresentationForms": {0xFB00, 0xFB4F}, "ArabicPresentationForms-A": {0xFB50, 0xFDFF},
	"CombiningHalfMarks": {0xFE20, 0xFE2F}, "CJKCompatibilityForms": {0xFE30, 0xFE4F}, "SmallFormVariants": {0xFE50, 0xFE6F},
	"ArabicPresentationForms-B": {0xFE70, 0xFEFE}, "Specials": {0xFEFF, 0xFEFF, 0xFFF0, 0xFFFD},
	"HalfwidthandFullwidthForms": {0xFF00, 0xFFEF}, "OldItalic": {0x10300, 0x1032F}, "Gothic": {0x10330, 0x1034F},
	"Deseret": {0x10400, 0x1044F}, "ByzantineMusicalSymbols": {0x1D000, 0x1D0FF}, "MusicalSymbols": {0x1D100, 0x1D1FF},
	"MathematicalAlphanumericSymbols": {0x1D400, 0x1D7FF}, "CJKUnifiedIdeographsExtensionB": {0x20000, 0x2A6D6},
	"CJKCompatibilityIdeographsSupplement": {0x2F800, 0x2FA1F}, "Tags": {0xE0000, 0xE007F},
}
