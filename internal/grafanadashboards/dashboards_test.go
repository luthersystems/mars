// Package grafanadashboards checks the Grafana dashboards in
// grafana-dashboards/. The grafana_dashboard_dir lookup plugin loads every
// file there as JSON.
package grafanadashboards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestDashboardQueries checks the PromQL in every dashboard panel target.
func TestDashboardQueries(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "grafana-dashboards")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("no dashboards in %s", dir)
	}

	byLe := regexp.MustCompile(`by\s*\(\s*le\b`)
	// One gateway client mode exports fabricclient_*, the other
	// fabricgateway_*. A bare name shows data for one mode only.
	bareGatewayName := regexp.MustCompile(`\bfabric(client|gateway)_\w+`)

	for _, entry := range entries {
		name := entry.Name()
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var doc any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			for _, expr := range exprs(doc) {
				if strings.Contains(expr, "histogram_quantile(") {
					if !strings.Contains(expr, "rate(") || !byLe.MatchString(expr) {
						t.Errorf("histogram_quantile needs rate() and a by (le) sum: %s", expr)
					}
				}
				if m := bareGatewayName.FindString(expr); m != "" {
					t.Errorf("%s matches one gateway client mode only; use {__name__=~\"fabric(client|gateway)_...\"}: %s", m, expr)
				}
			}
		})
	}
}

// TestFabricEndorsementPanel checks the metric name of the endorsement
// duration panel. Fabric peers export endorser_proposal_duration.
func TestFabricEndorsementPanel(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "grafana-dashboards", "fabric.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	found := false
	for _, expr := range exprs(doc) {
		if strings.Contains(expr, "endorser_") {
			found = true
			if !strings.Contains(expr, "endorser_proposal_duration_bucket") {
				t.Errorf("endorsement panel queries an unknown metric: %s", expr)
			}
		}
	}
	if !found {
		t.Error("fabric.json has no endorsement duration query")
	}
}

// exprs returns every "expr" string in a decoded dashboard.
func exprs(v any) []string {
	var out []string
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			if s, ok := child.(string); ok && k == "expr" {
				out = append(out, s)
				continue
			}
			out = append(out, exprs(child)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, exprs(child)...)
		}
	}
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
