package xsdlite

import (
	"errors"
	"strconv"
	"strings"
)

// Name characters from XML 1.0 (fifth edition), as inclusive rune pairs.
var nameStartRanges = []rune{
	':', ':', 'A', 'Z', '_', '_', 'a', 'z', 0xC0, 0xD6, 0xD8, 0xF6, 0xF8, 0x2FF, 0x370, 0x37D, 0x37F, 0x1FFF,
	0x200C, 0x200D, 0x2070, 0x218F, 0x2C00, 0x2FEF, 0x3001, 0xD7FF, 0xF900, 0xFDCF, 0xFDF0, 0xFFFD, 0x10000, 0xEFFFF,
}

var nameExtraRanges = []rune{'-', '.', '0', '9', 0xB7, 0xB7, 0x300, 0x36F, 0x203F, 0x2040}

func inRanges(r rune, ranges []rune) bool {
	for i := 0; i < len(ranges); i += 2 {
		if r >= ranges[i] && r <= ranges[i+1] {
			return true
		}
	}
	return false
}

func isNameStart(r rune) bool { return inRanges(r, nameStartRanges) }
func isNameChar(r rune) bool  { return isNameStart(r) || inRanges(r, nameExtraRanges) }

func isNCName(s string) bool {
	for i, r := range s {
		if r == ':' || (i == 0 && !isNameStart(r)) || !isNameChar(r) {
			return false
		}
	}
	return s != ""
}

func lexicalOK(lex uint8, v string) bool {
	if v == "" {
		return false
	}
	switch lex {
	case lexName:
		for i, r := range v {
			if (i == 0 && !isNameStart(r)) || !isNameChar(r) {
				return false
			}
		}
	case lexNCName:
		return isNCName(v)
	case lexNMTOKEN:
		for _, r := range v {
			if !isNameChar(r) {
				return false
			}
		}
	case lexLanguage:
		for i, part := range strings.Split(v, "-") {
			if len(part) < 1 || len(part) > 8 {
				return false
			}
			for _, r := range part {
				letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
				if !letter && (i == 0 || r < '0' || r > '9') {
					return false
				}
			}
		}
	}
	return true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parsePrimitive checks the lexical form of a primitive value and returns its
// canonical form, in which equal values are equal strings.
func parsePrimitive(p prim, v string, ns resolver) (string, bool) {
	switch p {
	case pBoolean:
		switch v {
		case "true", "1":
			return "true", true
		case "false", "0":
			return "false", true
		}
		return "", false
	case pDecimal:
		return parseDecimal(v)
	case pFloat:
		return parseFloat(v, 32)
	case pDouble:
		return parseFloat(v, 64)
	case pDuration:
		return v, isDuration(v)
	case pDateTime, pTime, pDate, pGYearMonth, pGYear, pGMonthDay, pGDay, pGMonth:
		return parseDate(p, v)
	case pHexBinary:
		if len(v)%2 != 0 {
			return "", false
		}
		for i := 0; i < len(v); i++ {
			c := v[i]
			if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') && !(c >= 'A' && c <= 'F') {
				return "", false
			}
		}
		return strings.ToUpper(v), true
	case pBase64:
		packed := strings.ReplaceAll(v, " ", "")
		if len(packed)%4 != 0 {
			return "", false
		}
		body := strings.TrimRight(packed, "=")
		if len(packed)-len(body) > 2 {
			return "", false
		}
		for i := 0; i < len(body); i++ {
			c := body[i]
			if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && c != '+' && c != '/' {
				return "", false
			}
		}
		return packed, true
	case pAnyURI:
		return v, isURIReference(v)
	case pQName, pNotation:
		prefix, local, found := strings.Cut(v, ":")
		if !found {
			prefix, local = "", v
		}
		if !isNCName(local) || (found && !isNCName(prefix)) {
			return "", false
		}
		if ns == nil {
			return v, true
		}
		uri, ok := ns.resolvePrefix(prefix)
		if !ok && prefix != "" {
			return "", false
		}
		return "{" + uri + "}" + local, true
	}
	return v, true
}

// isURIReference applies the two rules of RFC 3986 that separate a URI
// reference from arbitrary text: a colon in the first path segment must end
// a scheme, and a percent sign must start an escape.
func isURIReference(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] == '%' && (i+2 >= len(v) || !isHex(v[i+1]) || !isHex(v[i+2])) {
			return false
		}
	}
	end := strings.IndexAny(v, "/?#")
	if end < 0 {
		end = len(v)
	}
	colon := strings.IndexByte(v[:end], ':')
	if colon < 0 {
		return true
	}
	for i := 0; i < colon; i++ {
		c := v[i]
		letter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !letter && (i == 0 || !((c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.')) {
			return false
		}
	}
	return colon > 0
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func parseDecimal(v string) (string, bool) {
	s, negative := v, false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		negative, s = s[0] == '-', s[1:]
	}
	whole, fraction, _ := strings.Cut(s, ".")
	if (whole == "" && fraction == "") || !allDigits(whole) || !allDigits(fraction) {
		return "", false
	}
	whole, fraction = strings.TrimLeft(whole, "0"), strings.TrimRight(fraction, "0")
	if whole == "" {
		whole = "0"
	}
	key := whole
	if fraction != "" {
		key += "." + fraction
	}
	if negative && key != "0" {
		key = "-" + key
	}
	return key, true
}

func compareDecimal(a, b string) int {
	an, bn := strings.HasPrefix(a, "-"), strings.HasPrefix(b, "-")
	switch {
	case an && !bn:
		return -1
	case bn && !an:
		return 1
	case an:
		return compareMagnitude(b[1:], a[1:])
	}
	return compareMagnitude(a, b)
}

func compareMagnitude(a, b string) int {
	aw, af, _ := strings.Cut(a, ".")
	bw, bf, _ := strings.Cut(b, ".")
	if len(aw) != len(bw) {
		if len(aw) < len(bw) {
			return -1
		}
		return 1
	}
	if c := strings.Compare(aw, bw); c != 0 {
		return c
	}
	return strings.Compare(af, bf)
}

func parseFloat(v string, bits int) (string, bool) {
	switch v {
	case "INF":
		return "+Inf", true
	case "-INF":
		return "-Inf", true
	case "NaN":
		return "NaN", true
	}
	mantissa, exponent := v, ""
	if i := strings.IndexAny(v, "eE"); i >= 0 {
		mantissa, exponent = v[:i], v[i+1:]
		digits := strings.TrimLeft(exponent, "+-")
		if digits == "" || !allDigits(digits) || len(exponent)-len(digits) > 1 {
			return "", false
		}
	}
	if _, ok := parseDecimal(mantissa); !ok {
		return "", false
	}
	f, err := strconv.ParseFloat(v, bits)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return "", false
	}
	return strconv.FormatFloat(f, 'g', -1, bits), true
}

func isDuration(v string) bool {
	s := strings.TrimPrefix(v, "-")
	if !strings.HasPrefix(s, "P") {
		return false
	}
	date, clock, hasClock := strings.Cut(s[1:], "T")
	if hasClock && clock == "" {
		return false
	}
	parts := 0
	eat := func(s string, units string, allowFraction bool) bool {
		for s != "" {
			i := 0
			for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || (allowFraction && s[i] == '.')) {
				i++
			}
			if i == 0 || i == len(s) {
				return false
			}
			at := strings.IndexByte(units, s[i])
			if at < 0 {
				return false
			}
			number := s[:i]
			if strings.Contains(number, ".") && (s[i] != 'S' || strings.Count(number, ".") > 1 || strings.HasPrefix(number, ".") || strings.HasSuffix(number, ".")) {
				return false
			}
			units, s = units[at+1:], s[i+1:]
			parts++
		}
		return true
	}
	return eat(date, "YMD", false) && eat(clock, "HMS", true) && parts > 0
}

// dateScan reads the fixed-width numeric fields of the date and time types.
type dateScan struct {
	s  string
	i  int
	ok bool
}

func (d *dateScan) number(width int) int {
	if d.i+width > len(d.s) || !allDigits(d.s[d.i:d.i+width]) {
		d.ok = false
		return 0
	}
	n, _ := strconv.Atoi(d.s[d.i : d.i+width])
	d.i += width
	return n
}

func (d *dateScan) literal(c byte) {
	if d.i >= len(d.s) || d.s[d.i] != c {
		d.ok = false
		return
	}
	d.i++
}

func (d *dateScan) year() int64 {
	negative := false
	if d.i < len(d.s) && d.s[d.i] == '-' {
		negative = true
		d.i++
	}
	start := d.i
	for d.i < len(d.s) && d.s[d.i] >= '0' && d.s[d.i] <= '9' {
		d.i++
	}
	digits := d.s[start:d.i]
	if len(digits) < 4 || len(digits) > 12 || (len(digits) > 4 && digits[0] == '0') {
		d.ok = false
		return 0
	}
	y, _ := strconv.ParseInt(digits, 10, 64)
	if y == 0 {
		d.ok = false
	}
	if negative {
		return -y
	}
	return y
}

func daysInMonth(year int64, month int) int {
	switch month {
	case 4, 6, 9, 11:
		return 30
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	}
	return 31
}

// daysFromCivil counts days from 1970-01-01 in the proleptic Gregorian calendar.
func daysFromCivil(y int64, m, d int) int64 {
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 {
		era = (y - 399) / 400
	}
	yoe := y - era*400
	mp := int64((m + 9) % 12)
	doy := (153*mp+2)/5 + int64(d) - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe - 719468
}

// parseDate checks a date or time value. dateTime, date and time get a
// canonical form that orders them on the time line; the others keep their
// lexical form.
func parseDate(p prim, v string) (string, bool) {
	d := &dateScan{s: v, ok: true}
	var year int64 = 4 // a leap year, for types that carry no year
	month, day, hour, minute, second := 1, 1, 0, 0, 0
	fraction := ""
	if p == pDateTime || p == pDate || p == pGYearMonth || p == pGYear {
		year = d.year()
	}
	switch p {
	case pDateTime, pDate:
		d.literal('-')
		month = d.number(2)
		d.literal('-')
		day = d.number(2)
	case pGYearMonth:
		d.literal('-')
		month = d.number(2)
	case pGMonthDay:
		d.literal('-')
		d.literal('-')
		month = d.number(2)
		d.literal('-')
		day = d.number(2)
	case pGDay:
		d.literal('-')
		d.literal('-')
		d.literal('-')
		day = d.number(2)
	case pGMonth:
		d.literal('-')
		d.literal('-')
		month = d.number(2)
	}
	if p == pDateTime {
		d.literal('T')
	}
	if p == pDateTime || p == pTime {
		hour = d.number(2)
		d.literal(':')
		minute = d.number(2)
		d.literal(':')
		second = d.number(2)
		if d.ok && d.i < len(d.s) && d.s[d.i] == '.' {
			start := d.i + 1
			for d.i = start; d.i < len(d.s) && d.s[d.i] >= '0' && d.s[d.i] <= '9'; d.i++ {
			}
			if fraction = d.s[start:d.i]; fraction == "" {
				d.ok = false
			}
		}
	}
	zone, offset := "", 0
	if d.ok && d.i < len(d.s) {
		switch c := d.s[d.i]; c {
		case 'Z':
			d.i++
			zone = "Z"
		case '+', '-':
			d.i++
			h := d.number(2)
			d.literal(':')
			m := d.number(2)
			if h > 14 || m > 59 || (h == 14 && m != 0) {
				d.ok = false
			}
			offset = h*60 + m
			if c == '-' {
				offset = -offset
			}
			zone = "Z"
		}
	}
	if !d.ok || d.i != len(d.s) || month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month) || minute > 59 || second > 59 {
		return "", false
	}
	if hour > 24 || (hour == 24 && (minute != 0 || second != 0 || strings.Trim(fraction, "0") != "")) {
		return "", false
	}
	if p != pDateTime && p != pDate && p != pTime {
		return v, true
	}
	if p == pTime {
		year, month, day = 1970, 1, 1
	}
	seconds := daysFromCivil(year, month, day)*86400 + int64(hour)*3600 + int64(minute)*60 + int64(second) - int64(offset)*60
	return strconv.FormatInt(seconds, 10) + "." + strings.TrimRight(fraction, "0") + zone, true
}

func ordered(p prim) bool {
	switch p {
	case pDecimal, pFloat, pDouble, pDateTime, pDate, pTime:
		return true
	}
	return false
}

// compareKeys orders two canonical values of one primitive type. Date and
// time values without a time zone are compared as if they were UTC.
func compareKeys(p prim, a, b string) (int, bool) {
	switch p {
	case pDecimal:
		return compareDecimal(a, b), true
	case pFloat, pDouble:
		x, errX := strconv.ParseFloat(a, 64)
		y, errY := strconv.ParseFloat(b, 64)
		if errX != nil || errY != nil || x != x || y != y {
			return 0, false
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	case pDateTime, pDate, pTime:
		as, af, _ := strings.Cut(strings.TrimSuffix(a, "Z"), ".")
		bs, bf, _ := strings.Cut(strings.TrimSuffix(b, "Z"), ".")
		x, _ := strconv.ParseInt(as, 10, 64)
		y, _ := strconv.ParseInt(bs, 10, 64)
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return strings.Compare(af, bf), true
	}
	return 0, false
}
