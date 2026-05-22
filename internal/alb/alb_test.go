package alb

import "testing"

func TestLutherEnvMapsIntegrationTag(t *testing.T) {
	if got := lutherEnv("integration"); got != "integ" {
		t.Fatalf("lutherEnv(integration) = %q, want integ", got)
	}
	if got := lutherEnv("dev"); got != "dev" {
		t.Fatalf("lutherEnv(dev) = %q, want dev", got)
	}
}

func TestMatchesTagsIgnoresEmptyRequestedValues(t *testing.T) {
	tags := map[string]string{
		"Project":     "mars",
		"Environment": "dev",
	}
	match := map[string]string{
		"Project":      "mars",
		"Environment":  "dev",
		"Component":    "",
		"Organization": "",
	}
	if !matchesTags(tags, match) {
		t.Fatal("matchesTags returned false for matching non-empty tags")
	}
	match["Environment"] = "prod"
	if matchesTags(tags, match) {
		t.Fatal("matchesTags returned true for mismatched non-empty tag")
	}
}
