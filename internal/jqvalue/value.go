// Package jqvalue models JSON values the way jq 1.7 sees them, so that Go
// ports of the sandbox-infrastructure-template shell scripts write the same
// bytes the scripts' jq filters wrote.
//
// The scripts' artifacts (drift.json, plan-summary.json) and log lines are
// read by ui-core and reliable, and the Go ports are checked against the
// shell byte for byte. encoding/json cannot do that: it reorders nothing but
// loses number literals (jq 1.7 keeps them, printing 1e2 as 1E+2 and 1.0 as
// 1.0), escapes differently (<, >, &, U+2028) and has no jq equality (objects
// compare as sets, numbers compare exactly as decimals). This package keeps
// an object's key order and each number's literal text, and implements the
// few jq operations the ports need, with jq's error texts.
package jqvalue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Kind is a JSON value's jq type.
type Kind int

const (
	Null Kind = iota
	False
	True
	Number
	String
	Array
	Object
)

// TypeName is jq's name for the kind, as its error messages print it.
func (k Kind) TypeName() string {
	switch k {
	case Null:
		return "null"
	case False, True:
		return "boolean"
	case Number:
		return "number"
	case String:
		return "string"
	case Array:
		return "array"
	default:
		return "object"
	}
}

// Value is one JSON value. Num holds a number's literal text exactly as it
// was read (or as an integer was computed); Str a string; Elems an array;
// Keys and Members an object, Keys in jq's (insertion) order.
type Value struct {
	Kind    Kind
	Num     string
	Str     string
	Elems   []*Value
	Keys    []string
	Members map[string]*Value
}

var nullValue = &Value{Kind: Null}

// NullV returns JSON null.
func NullV() *Value { return nullValue }

// Bool returns true or false.
func Bool(b bool) *Value {
	if b {
		return &Value{Kind: True}
	}
	return &Value{Kind: False}
}

// Int returns a computed integer, printed as jq prints a whole number.
func Int(n int) *Value { return &Value{Kind: Number, Num: strconv.Itoa(n)} }

// Str returns a string value.
func Str(s string) *Value { return &Value{Kind: String, Str: s} }

// Arr returns an array value.
func Arr(elems ...*Value) *Value { return &Value{Kind: Array, Elems: elems} }

// NewObject returns an empty object.
func NewObject() *Value { return &Value{Kind: Object, Members: map[string]*Value{}} }

// Set sets key to v. As in jq, a key that is already there keeps its place.
func (v *Value) Set(key string, val *Value) {
	if _, ok := v.Members[key]; !ok {
		v.Keys = append(v.Keys, key)
	}
	v.Members[key] = val
}

// Get returns the member key of an object, or nil when there is none.
func (v *Value) Get(key string) *Value {
	if v.Kind != Object {
		return nil
	}
	return v.Members[key]
}

// Truthy is jq's truth: everything except null and false.
func (v *Value) Truthy() bool { return v.Kind != Null && v.Kind != False }

// Error is a jq runtime error. Message is jq's text without the
// "jq: error (at <stdin>:N): " prefix, which the caller adds.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func errorf(format string, a ...any) error { return &Error{Message: fmt.Sprintf(format, a...)} }

// ErrNoInput reports input that holds no JSON value at all. jq then runs its
// filter zero times and prints nothing, which the scripts rely on.
var ErrNoInput = errors.New("no JSON input")

// Parse reads one JSON value. Whitespace-only input returns ErrNoInput.
// Invalid UTF-8 in strings becomes U+FFFD, as in jq.
func Parse(data []byte) (*Value, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, ErrNoInput
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		// jq would run its filter once per value; the ports read terraform's
		// single document, so a second value is refused instead.
		return nil, errors.New("unexpected data after the JSON value")
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (*Value, error) {
	tok, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	switch t := tok.(type) {
	case nil:
		return NullV(), nil
	case bool:
		return Bool(t), nil
	case json.Number:
		return &Value{Kind: Number, Num: string(t)}, nil
	case string:
		return Str(t), nil
	case json.Delim:
		switch t {
		case '[':
			arr := Arr()
			for dec.More() {
				e, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr.Elems = append(arr.Elems, e)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		case '{':
			obj := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string: %v", kt)
				}
				e, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				obj.Set(key, e)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return obj, nil
		}
	}
	return nil, fmt.Errorf("unexpected token %v", tok)
}

// decimal is a number literal split the way decNumber holds it: a sign, a
// coefficient without leading zeros ("0" for zero) and an exponent.
type decimal struct {
	neg   bool
	coeff string
	exp   int
}

func parseDecimal(lit string) decimal {
	var d decimal
	s := lit
	if strings.HasPrefix(s, "-") {
		d.neg = true
		s = s[1:]
	}
	mant, expPart := s, ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant, expPart = s[:i], s[i+1:]
	}
	intPart, frac := mant, ""
	if i := strings.IndexByte(mant, '.'); i >= 0 {
		intPart, frac = mant[:i], mant[i+1:]
	}
	if expPart != "" {
		// A JSON exponent is digits with an optional sign; one that
		// overflows int is far outside decNumber's range anyway.
		e, _ := strconv.Atoi(strings.TrimPrefix(expPart, "+"))
		d.exp = e
	}
	d.exp -= len(frac)
	d.coeff = strings.TrimLeft(intPart+frac, "0")
	if d.coeff == "" {
		d.coeff = "0"
	}
	return d
}

// sciString is decNumber's to-scientific-string, which jq 1.7 uses to print
// a number literal: plain notation while the exponent is not positive and
// the adjusted exponent is at least -6, else d.ddddE±n.
func (d decimal) sciString() string {
	var b strings.Builder
	if d.neg {
		b.WriteByte('-')
	}
	adj := d.exp + len(d.coeff) - 1
	if d.exp <= 0 && adj >= -6 {
		if d.exp == 0 {
			b.WriteString(d.coeff)
			return b.String()
		}
		pos := len(d.coeff) + d.exp
		if pos > 0 {
			b.WriteString(d.coeff[:pos])
			b.WriteByte('.')
			b.WriteString(d.coeff[pos:])
		} else {
			b.WriteString("0.")
			b.WriteString(strings.Repeat("0", -pos))
			b.WriteString(d.coeff)
		}
		return b.String()
	}
	b.WriteByte(d.coeff[0])
	if len(d.coeff) > 1 {
		b.WriteByte('.')
		b.WriteString(d.coeff[1:])
	}
	b.WriteByte('E')
	if adj >= 0 {
		b.WriteByte('+')
	}
	b.WriteString(strconv.Itoa(adj))
	return b.String()
}

// canonical drops trailing zeros so that equal numbers have equal parts.
func (d decimal) canonical() decimal {
	if d.coeff == "0" {
		return decimal{coeff: "0"}
	}
	trimmed := strings.TrimRight(d.coeff, "0")
	d.exp += len(d.coeff) - len(trimmed)
	d.coeff = trimmed
	return d
}

// Equal is jq's ==. Objects compare as sets of members, numbers exactly as
// decimals (jq 1.7 compares two literals with decNumber), so 1.0 == 1 and
// -0 == 0 but 100000000000000000001 != 100000000000000000000.
func Equal(a, b *Value) bool {
	ka, kb := a.Kind, b.Kind
	if ka != kb {
		return false
	}
	switch ka {
	case Null, False, True:
		return true
	case Number:
		return parseDecimal(a.Num).canonical() == parseDecimal(b.Num).canonical()
	case String:
		return a.Str == b.Str
	case Array:
		if len(a.Elems) != len(b.Elems) {
			return false
		}
		for i := range a.Elems {
			if !Equal(a.Elems[i], b.Elems[i]) {
				return false
			}
		}
		return true
	default:
		if len(a.Keys) != len(b.Keys) {
			return false
		}
		for _, k := range a.Keys {
			bv, ok := b.Members[k]
			if !ok || !Equal(a.Members[k], bv) {
				return false
			}
		}
		return true
	}
}

// IsZero reports a number equal to 0.
func (v *Value) IsZero() bool { return v.Kind == Number && parseDecimal(v.Num).coeff == "0" }

// Index is jq's .[key] with a string key: null on null, the member (or null)
// on an object, else jq's error.
func Index(v *Value, key string) (*Value, error) {
	switch v.Kind {
	case Null:
		return NullV(), nil
	case Object:
		if m, ok := v.Members[key]; ok {
			return m, nil
		}
		return NullV(), nil
	default:
		return nil, errorf("Cannot index %s with string %q", v.Kind.TypeName(), key)
	}
}

// IndexBy is jq's $obj[$key] for a key of any type.
func IndexBy(v, key *Value) (*Value, error) {
	if key.Kind == String {
		return Index(v, key.Str)
	}
	if v.Kind == Null && key.Kind != Object && key.Kind != String {
		// null[anything] is null in jq, except the cases above.
		return NullV(), nil
	}
	return nil, errorf("Cannot index %s with %s", v.Kind.TypeName(), key.Kind.TypeName())
}

// Iterate is jq's .[]: an array's elements, an object's values, else an error.
func Iterate(v *Value) ([]*Value, error) {
	switch v.Kind {
	case Array:
		return v.Elems, nil
	case Object:
		out := make([]*Value, 0, len(v.Keys))
		for _, k := range v.Keys {
			out = append(out, v.Members[k])
		}
		return out, nil
	case Null:
		return nil, errorf("Cannot iterate over null")
	default:
		return nil, errorf("Cannot iterate over %s (%s)", v.Kind.TypeName(), Trunc(v))
	}
}

// Alt is jq's a // b for a single-valued a.
func Alt(a, b *Value) *Value {
	if a.Truthy() {
		return a
	}
	return b
}

// Entry is one to_entries element.
type Entry struct {
	Key   *Value
	Value *Value
}

// ToEntries is jq's to_entries: an object's members in order, or an array's
// elements keyed by index; other values have no keys.
func ToEntries(v *Value) ([]Entry, error) {
	switch v.Kind {
	case Object:
		out := make([]Entry, 0, len(v.Keys))
		for _, k := range v.Keys {
			out = append(out, Entry{Key: Str(k), Value: v.Members[k]})
		}
		return out, nil
	case Array:
		out := make([]Entry, 0, len(v.Elems))
		for i, e := range v.Elems {
			out = append(out, Entry{Key: Int(i), Value: e})
		}
		return out, nil
	default:
		return nil, errorf("%s (%s) has no keys", v.Kind.TypeName(), Trunc(v))
	}
}

// Trunc is jq's jv_dump_string_trunc with its 15-byte buffer: the compact
// dump, cut to 11 bytes plus "..." when it is longer than 14 bytes.
func Trunc(v *Value) string {
	s := Compact(v)
	if len(s) > 14 {
		return s[:11] + "..."
	}
	return s
}

// Compact is jq -c (and tojson).
func Compact(v *Value) string {
	var b strings.Builder
	dump(&b, v, -1, 0)
	return b.String()
}

// Pretty is jq's default output: two-space indent, no trailing newline.
func Pretty(v *Value) string {
	var b strings.Builder
	dump(&b, v, 2, 0)
	return b.String()
}

// Interpolate is jq's \(v) inside a string: a string as is, else tojson.
func Interpolate(v *Value) string {
	if v.Kind == String {
		return v.Str
	}
	return Compact(v)
}

func dump(b *strings.Builder, v *Value, indent, level int) {
	switch v.Kind {
	case Null:
		b.WriteString("null")
	case False:
		b.WriteString("false")
	case True:
		b.WriteString("true")
	case Number:
		b.WriteString(parseDecimal(v.Num).sciString())
	case String:
		writeString(b, v.Str)
	case Array:
		if len(v.Elems) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, e := range v.Elems {
			if i > 0 {
				b.WriteByte(',')
			}
			newline(b, indent, level+1)
			dump(b, e, indent, level+1)
		}
		newline(b, indent, level)
		b.WriteByte(']')
	case Object:
		if len(v.Keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, k := range v.Keys {
			if i > 0 {
				b.WriteByte(',')
			}
			newline(b, indent, level+1)
			writeString(b, k)
			b.WriteByte(':')
			if indent >= 0 {
				b.WriteByte(' ')
			}
			dump(b, v.Members[k], indent, level+1)
		}
		newline(b, indent, level)
		b.WriteByte('}')
	}
}

func newline(b *strings.Builder, indent, level int) {
	if indent < 0 {
		return
	}
	b.WriteByte('\n')
	b.WriteString(strings.Repeat(" ", indent*level))
}

// writeString escapes as jq does: \" \\ \b \f \n \r \t, \u00xx for the
// other control characters and DEL, everything else as raw UTF-8.
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}
