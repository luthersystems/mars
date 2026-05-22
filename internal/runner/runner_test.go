package runner

import "testing"

func TestShellQuoteMatchesShellSafeArgs(t *testing.T) {
	tests := map[string]string{
		"":              "''",
		"plain":         "plain",
		"vars/dev.tf":   "vars/dev.tf",
		"a b":           "'a b'",
		"can't":         "'can'\"'\"'t'",
		"-out=plan.out": "-out=plan.out",
	}
	for input, expected := range tests {
		if got := ShellQuote(input); got != expected {
			t.Fatalf("ShellQuote(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestScriptStringJoinsCommands(t *testing.T) {
	got := ScriptString(Cmd("terraform", "workspace", "select", "dev"), Cmd("terraform", "plan", "-var-file=vars/dev/main.tfvars"))
	want := "terraform workspace select dev && terraform plan -var-file=vars/dev/main.tfvars"
	if got != want {
		t.Fatalf("ScriptString() = %q, want %q", got, want)
	}
}
