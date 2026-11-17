package jqvalue

import "testing"

// The expected texts are what jq 1.7 prints (checked by hand with
// `echo '[<lit>]' | jq -c .`); the template's scripts wrote them.
func TestNumberLiteralsPrintAsJQ(t *testing.T) {
	cases := map[string]string{
		"0":                      "0",
		"-0":                     "-0",
		"1.0":                    "1.0",
		"1.000":                  "1.000",
		"0.0":                    "0.0",
		"0.00":                   "0.00",
		"1e2":                    "1E+2",
		"1E2":                    "1E+2",
		"1.5e3":                  "1.5E+3",
		"0e5":                    "0E+5",
		"0.0000001":              "1E-7",
		"0.000001":               "0.000001",
		"1E-7":                   "1E-7",
		"100":                    "100",
		"100000000000000000001":  "100000000000000000001",
		"12345678901234567890.5": "12345678901234567890.5",
		"1.7976931348623157e309": "1.7976931348623157E+309",
		"-12.50":                 "-12.50",
		"1e-2":                   "0.01",
		"123e-5":                 "0.00123",
	}
	for lit, want := range cases {
		if got := Compact(&Value{Kind: Number, Num: lit}); got != want {
			t.Errorf("%s printed %s, want %s", lit, got, want)
		}
	}
}

func TestEqualIsJQEquality(t *testing.T) {
	must := func(s string) *Value {
		v, err := Parse([]byte(s))
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		return v
	}
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0", "1", true},
		{"-0", "0", true},
		{"0e5", "0", true},
		{"1e2", "100", true},
		{"100000000000000000001", "100000000000000000000", false},
		{"1e-400", "0", false},
		{`{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{`{"a":1}`, `{"a":1,"b":null}`, false},
		{`[1,2]`, `[2,1]`, false},
		{`null`, `false`, false},
		{`"a"`, `"a"`, true},
	}
	for _, c := range cases {
		if got := Equal(must(c.a), must(c.b)); got != c.want {
			t.Errorf("%s == %s: %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDumpEscapesAndKeepsKeyOrder(t *testing.T) {
	v, err := Parse([]byte(`{"z":"tab\there \"q\" \\ \u0001\u007f/<&> é","a":[],"m":{},"dup":1,"dup":2}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"z":"tab\there \"q\" \\ \u0001\u007f/<&>` + " é" + `","a":[],"m":{},"dup":2}`
	if got := Compact(v); got != want {
		t.Errorf("compact:\n got %s\nwant %s", got, want)
	}
	wantPretty := "{\n  \"a\": [\n    1,\n    {\n      \"b\": null\n    }\n  ],\n  \"e\": []\n}"
	p, _ := Parse([]byte(`{"a":[1,{"b":null}],"e":[]}`))
	if got := Pretty(p); got != wantPretty {
		t.Errorf("pretty:\n%s\nwant\n%s", got, wantPretty)
	}
}

func TestParseEmptyInput(t *testing.T) {
	for _, in := range []string{"", "\n", "  \n\t"} {
		if _, err := Parse([]byte(in)); err != ErrNoInput {
			t.Errorf("Parse(%q) = %v, want ErrNoInput", in, err)
		}
	}
	if _, err := Parse([]byte(`{"a":`)); err == nil {
		t.Error("truncated JSON parsed")
	}
	if _, err := Parse([]byte(`{} {}`)); err == nil {
		t.Error("two documents parsed")
	}
}

func TestErrorTexts(t *testing.T) {
	s, _ := Parse([]byte(`"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`))
	if _, err := Iterate(s); err == nil || err.Error() != `Cannot iterate over string ("aaaaaaaaaa...)` {
		t.Errorf("iterate: %v", err)
	}
	if _, err := Index(Arr(), "x"); err == nil || err.Error() != `Cannot index array with string "x"` {
		t.Errorf("index: %v", err)
	}
	if _, err := ToEntries(Int(5)); err == nil || err.Error() != `number (5) has no keys` {
		t.Errorf("to_entries: %v", err)
	}
	if _, err := IndexBy(NewObject(), NullV()); err == nil || err.Error() != `Cannot index object with null` {
		t.Errorf("index by null: %v", err)
	}
}
