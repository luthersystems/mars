package vaulthelper

import (
	"bytes"
	"strings"
	"testing"
)

func TestRequiredEnvReturnsValues(t *testing.T) {
	values, err := RequiredEnv(func(key string) string {
		return map[string]string{"A": "one", "B": "two"}[key]
	}, "A", "B")
	if err != nil {
		t.Fatal(err)
	}
	if values["A"] != "one" || values["B"] != "two" {
		t.Fatalf("values = %#v", values)
	}
}

func TestRequiredEnvReportsMissingName(t *testing.T) {
	_, err := RequiredEnv(func(string) string { return "" }, "AWS_REGION")
	if err == nil || !strings.Contains(err.Error(), "AWS_REGION") {
		t.Fatalf("error = %v, want missing AWS_REGION", err)
	}
}

func TestPrintSecretWritesOnlySecret(t *testing.T) {
	var stdout bytes.Buffer
	if err := PrintSecret(&stdout, "password"); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); got != "password" {
		t.Fatalf("stdout = %q", got)
	}
}
