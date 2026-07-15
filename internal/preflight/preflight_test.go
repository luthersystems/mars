package preflight

import (
	"reflect"
	"testing"
)

// TestUsageErrors pins the exit-code contract: bad flags, empty subcommands,
// empty permission/action lists, and an unreadable credentials file all return
// the usage exit code (2).
func TestUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no arguments", nil},
		{"unknown subcommand", []string{"azure", "--foo"}},
		{"gcp missing project-id", []string{"gcp", "--credentials-file", "/nope", "--permissions", "a"}},
		{"gcp missing credentials-file", []string{"gcp", "--project-id", "p", "--permissions", "a"}},
		{"gcp empty permissions", []string{"gcp", "--project-id", "p", "--credentials-file", "/nope"}},
		{"gcp unreadable credentials file", []string{"gcp", "--project-id", "p", "--credentials-file", "/does/not/exist.json", "--permissions", "a"}},
		{"gcp unknown flag", []string{"gcp", "--bogus"}},
		{"aws empty actions", []string{"aws", "--role-arn", "arn:aws:iam::1:role/x"}},
		{"aws unknown flag", []string{"aws", "--bogus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := runPreflight(t, tc.args...)
			if code != exitUsage {
				t.Errorf("Main(%v) = %d, want %d (usage)", tc.args, code, exitUsage)
			}
		})
	}
}

// TestHelpExitsZero confirms the top-level help path returns exit 0.
func TestHelpExitsZero(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		if code, _, _ := runPreflight(t, arg); code != exitOK {
			t.Errorf("Main(%q) = %d, want %d", arg, code, exitOK)
		}
	}
}

// TestStringListParsing pins comma-splitting + repeat accumulation + blank drop.
func TestStringListParsing(t *testing.T) {
	cases := []struct {
		name string
		sets []string
		want []string
	}{
		{"comma separated", []string{"a,b,c"}, []string{"a", "b", "c"}},
		{"repeated", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"mixed with spaces and blanks", []string{" a , b ", "", "c,"}, []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s stringList
			for _, v := range tc.sets {
				if err := s.Set(v); err != nil {
					t.Fatalf("Set(%q): %v", v, err)
				}
			}
			if !reflect.DeepEqual([]string(s), tc.want) {
				t.Errorf("stringList = %v, want %v", []string(s), tc.want)
			}
		})
	}
}
