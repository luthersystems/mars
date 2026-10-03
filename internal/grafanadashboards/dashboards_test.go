// Package grafanadashboards checks the Grafana dashboards in
// grafana-dashboards/. The grafana_dashboard_dir lookup plugin loads every
// file there as JSON.
package grafanadashboards

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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
	// fabricgateway_*. A query must read both: rate(fabricclient_x[w]) or
	// rate(fabricgateway_x[w]). A single name shows one mode only. A
	// __name__ regex across both prefixes fails ("vector cannot contain
	// metrics with the same labelset") when one target exports both,
	// because rate() drops the name.
	gatewayName := regexp.MustCompile(`\bfabric(client|gateway)_(\w+)`)
	nameRegexBothModes := regexp.MustCompile(`__name__\s*=~\s*"[^"]*fabric\(`)

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
				if nameRegexBothModes.MatchString(expr) {
					t.Errorf("a __name__ regex across both gateway prefixes fails when one target exports both; use rate(fabricclient_x[5m]) or rate(fabricgateway_x[5m]): %s", expr)
				}
				for _, m := range gatewayName.FindAllStringSubmatch(expr, -1) {
					other := "fabricgateway_"
					if m[1] == "gateway" {
						other = "fabricclient_"
					}
					if !regexp.MustCompile(`\b` + other + regexp.QuoteMeta(m[2]) + `\b`).MatchString(expr) {
						t.Errorf("%s matches one gateway client mode only; add %s%s: %s", m[0], other, m[2], expr)
					}
				}
			}
		})
	}
}

// TestLegendLabels checks that a panel legend names only labels the query
// keeps. After sum by (a, b), every other label is gone, so {{c}} in the
// legend renders empty.
func TestLegendLabels(t *testing.T) {
	sumBy := regexp.MustCompile(`^\s*(?:sum|avg|min|max|count)\s+by\s*\(([^)]*)\)`)
	placeholder := regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)
	dir := filepath.Join(repoRoot(t), "grafana-dashboards")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		t.Run(name, func(t *testing.T) {
			for _, target := range targets(readDashboard(t, name)) {
				m := sumBy.FindStringSubmatch(target.expr)
				if m == nil {
					continue
				}
				kept := map[string]bool{}
				for _, label := range strings.Split(m[1], ",") {
					kept[strings.TrimSpace(label)] = true
				}
				for _, p := range placeholder.FindAllStringSubmatch(target.legend, -1) {
					if !kept[p[1]] {
						t.Errorf("legend %q uses {{%s}}, which %q drops", target.legend, p[1], m[0])
					}
				}
			}
		})
	}
}

// endorsementBuckets are the two names of the Fabric endorsement duration
// histogram. Fabric v1.4.2 to v1.4.4 export endorser_propsal_duration
// (misspelled). v1.4.5 and later export endorser_proposal_duration. A peer
// exports one of them, never both.
var endorsementBuckets = []string{"endorser_propsal_duration_bucket", "endorser_proposal_duration_bucket"}

// TestFabricEndorsementPanel checks that the endorsement duration panel
// selects both names of the histogram, so it shows data for every Fabric
// version that mars deploys.
func TestFabricEndorsementPanel(t *testing.T) {
	found := false
	for _, expr := range exprs(readDashboard(t, "fabric.json")) {
		if !strings.Contains(expr, "endorser_") {
			continue
		}
		found = true
		selectors, bare, err := metricNames(expr)
		if err != nil {
			t.Errorf("%v: %s", err, expr)
			continue
		}
		for _, name := range bare {
			t.Errorf("query names %s only; use {__name__=~\"endorser_(propsal|proposal)_duration_bucket\"}: %s", name, expr)
		}
		for _, want := range endorsementBuckets {
			matched := false
			for _, name := range bare {
				matched = matched || name == want
			}
			for _, sel := range selectors {
				re, err := regexp.Compile("^(?:" + sel + ")$")
				if err != nil {
					t.Errorf("__name__ selector %q: %v: %s", sel, err, expr)
					continue
				}
				matched = matched || re.MatchString(want)
			}
			if !matched {
				t.Errorf("endorsement query does not select %s: %s", want, expr)
			}
		}
	}
	if !found {
		t.Error("fabric.json has no endorsement duration query")
	}
}

// times1000RE matches a conversion from seconds to milliseconds.
var times1000RE = regexp.MustCompile(`\*\s*1000\b|\b1000\s*\*`)

// TestDurationPanelUnits checks the unit of each histogram_quantile panel
// against the unit the metric records. Fabric and Prometheus record
// *_duration and *_seconds histograms in seconds. Fabric observes
// endorser_proposal_duration with time.Since(startTime).Seconds() in
// core/endorser/endorser.go (v1.4.2 to v2.5.15). The gateway records
// *_ms histograms in milliseconds. A panel must show the recorded unit, or
// multiply seconds by 1000 and show ms.
func TestDurationPanelUnits(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "grafana-dashboards")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		for _, panel := range panels(readDashboard(t, name)) {
			targets, _ := panel["targets"].([]any)
			for _, raw := range targets {
				target, _ := raw.(map[string]any)
				expr, _ := target["expr"].(string)
				if !strings.Contains(expr, "histogram_quantile(") {
					continue
				}
				selectors, bare, err := metricNames(expr)
				if err != nil {
					t.Errorf("%s: %v: %s", name, err, expr)
					continue
				}
				for _, metric := range append(selectors, bare...) {
					want := recordedUnit(metric)
					if want == "" {
						continue
					}
					if want == "s" && times1000RE.MatchString(expr) {
						want = "ms"
					}
					checked++
					if got := targetUnit(panel, target); got != want {
						t.Errorf("%s: panel %q shows %s in unit %q, want %q: %s", name, panel["title"], metric, got, want, expr)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Error("no histogram_quantile panel over a duration metric")
	}
}

// recordedUnit returns the unit that a histogram records, from its name:
// "s", "ms", or "" if the name does not say. metric can be a __name__
// regexp.
func recordedUnit(metric string) string {
	name := strings.TrimSuffix(metric, "_bucket")
	switch {
	case strings.HasSuffix(name, "_seconds"), strings.HasSuffix(name, "_duration"):
		return "s"
	case strings.HasSuffix(name, "_ms"):
		return "ms"
	}
	return ""
}

// targetUnit returns the unit of the axis that shows target. A graph panel
// shows a series on its left y axis, unless a series override with the
// series legend as alias moves it to the right axis. Grafana renders a
// {{label}} in the legend, so only a literal legend matches an alias here.
// Other panels use fieldConfig.defaults.unit.
func targetUnit(panel, target map[string]any) string {
	yaxes, ok := panel["yaxes"].([]any)
	if !ok {
		fieldConfig, _ := panel["fieldConfig"].(map[string]any)
		defaults, _ := fieldConfig["defaults"].(map[string]any)
		unit, _ := defaults["unit"].(string)
		return unit
	}
	axis := 0
	legend, _ := target["legendFormat"].(string)
	overrides, _ := panel["seriesOverrides"].([]any)
	for _, raw := range overrides {
		override, _ := raw.(map[string]any)
		if alias, _ := override["alias"].(string); alias != legend {
			continue
		}
		if yaxis, ok := override["yaxis"].(float64); ok && yaxis == 2 {
			axis = 1
		}
	}
	if axis >= len(yaxes) {
		return ""
	}
	y, _ := yaxes[axis].(map[string]any)
	format, _ := y["format"].(string)
	return format
}

// panels returns every panel in a decoded dashboard: each object with a
// "targets" list, rows and nested panels included.
func panels(v any) []map[string]any {
	var out []map[string]any
	switch v := v.(type) {
	case map[string]any:
		if _, ok := v["targets"].([]any); ok {
			out = append(out, v)
		}
		for _, child := range v {
			out = append(out, panels(child)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, panels(child)...)
		}
	}
	return out
}

// The gateway exports these metrics in both client modes, with the prefix
// fabricclient_ or fabricgateway_. See substrate
// internal/shiroclient/fabricclient/fabricclient.go and
// internal/shiroclient/fabricgateway/metrics.go.
var (
	gatewayPrefixes   = []string{"fabricclient_", "fabricgateway_"}
	gatewayCounters   = []string{"tx_total", "tx_sim_total", "tx_commit_total", "tx_retry_total", "tx_sim_err_total", "tx_commit_err_total"}
	gatewayHistograms = []string{"tx_sim_dur_ms", "tx_commit_dur_ms"}
)

// otherShiroGWMetrics lists the other metrics that shiro-gw.json queries.
// The gateway does not export them. Their panels are older than this test.
var otherShiroGWMetrics = []string{"grpc_client_handled_total"}

// TestShiroGWMetricNames checks every metric name that shiro-gw.json
// queries. A gateway metric must be queried under both prefixes in the same
// expression. Any other name must be in otherShiroGWMetrics. __name__
// selectors are not used. The dashboard must query each gateway metric.
func TestShiroGWMetricNames(t *testing.T) {
	// series maps each series name to its gateway metric without prefix.
	series := map[string]string{}
	for _, prefix := range gatewayPrefixes {
		for _, name := range gatewayCounters {
			series[prefix+name] = name
		}
		for _, name := range gatewayHistograms {
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				series[prefix+name+suffix] = name
			}
		}
	}
	other := map[string]bool{}
	for _, name := range otherShiroGWMetrics {
		other[name] = true
	}

	queried := map[string]bool{}
	for _, expr := range exprs(readDashboard(t, "shiro-gw.json")) {
		selectors, bare, err := metricNames(expr)
		if err != nil {
			t.Errorf("%v: %s", err, expr)
			continue
		}
		if len(selectors)+len(bare) == 0 {
			t.Errorf("no metric name in query: %s", expr)
		}
		for _, sel := range selectors {
			t.Errorf("__name__ selector %q: query each gateway prefix by name instead: %s", sel, expr)
		}
		names := map[string]bool{}
		for _, name := range bare {
			names[name] = true
		}
		for _, name := range bare {
			metric, ok := series[name]
			if !ok {
				if !other[name] {
					t.Errorf("query names unknown metric %q: %s", name, expr)
				}
				continue
			}
			rest := strings.TrimPrefix(strings.TrimPrefix(name, "fabricclient_"), "fabricgateway_")
			both := true
			for _, prefix := range gatewayPrefixes {
				both = both && names[prefix+rest]
			}
			if both {
				queried[metric] = true
			}
		}
	}
	for _, name := range append(append([]string{}, gatewayCounters...), gatewayHistograms...) {
		if !queried[name] {
			t.Errorf("shiro-gw.json does not query gateway metric %s", name)
		}
	}
}

var (
	nameMatcherRE = regexp.MustCompile(`__name__\s*(=~|!~|!=|=)\s*("(?:[^"\\]|\\.)*")`)
	stringRE      = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)
	labelBlockRE  = regexp.MustCompile(`\{[^}]*\}`)
	rangeRE       = regexp.MustCompile(`\[[^\]]*\]`)
	labelListRE   = regexp.MustCompile(`\b(?:by|without|on|ignoring|group_left|group_right)\s*\([^)]*\)`)
)

// promqlWords are PromQL keywords that are not metric names.
var promqlWords = map[string]bool{
	"and": true, "or": true, "unless": true, "bool": true, "offset": true, "atan2": true,
	"by": true, "without": true, "on": true, "ignoring": true, "group_left": true, "group_right": true,
	"inf": true, "nan": true,
}

// metricNames returns the __name__ selector regexps and the bare metric
// names in a PromQL expression. An identifier followed by "(" is a
// function or an aggregation, not a metric name.
func metricNames(expr string) (selectors, bare []string, err error) {
	for _, m := range nameMatcherRE.FindAllStringSubmatch(expr, -1) {
		value, err := strconv.Unquote(m[2])
		if err != nil {
			return nil, nil, fmt.Errorf("__name__ value %s: %w", m[2], err)
		}
		switch m[1] {
		case "=~":
			selectors = append(selectors, value)
		case "=":
			selectors = append(selectors, regexp.QuoteMeta(value))
		default:
			return nil, nil, fmt.Errorf("negative __name__ matcher %s", m[0])
		}
	}

	s := stringRE.ReplaceAllString(expr, `""`)
	s = labelBlockRE.ReplaceAllString(s, " ")
	s = rangeRE.ReplaceAllString(s, " ")
	s = labelListRE.ReplaceAllString(s, " ")
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case isIdentStart(c):
			j := i + 1
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			k := j
			for k < len(s) && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n') {
				k++
			}
			if word := s[i:j]; (k == len(s) || s[k] != '(') && !promqlWords[word] {
				bare = append(bare, word)
			}
			i = j
		case c >= '0' && c <= '9', c == '.', c == '$':
			// A number (1e3) or a Grafana variable is one token.
			j := i + 1
			for j < len(s) && (isIdentChar(s[j]) || s[j] == '.') {
				j++
			}
			i = j
		default:
			i++
		}
	}
	return selectors, bare, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c == ':' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// readDashboard decodes grafana-dashboards/<name>.
func readDashboard(t *testing.T, name string) any {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "grafana-dashboards", name))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	return doc
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

type target struct{ expr, legend string }

// targets returns every object with an "expr" string in a decoded
// dashboard, with its "legendFormat" if it has one.
func targets(v any) []target {
	var out []target
	switch v := v.(type) {
	case map[string]any:
		if expr, ok := v["expr"].(string); ok {
			legend, _ := v["legendFormat"].(string)
			out = append(out, target{expr, legend})
		}
		for _, child := range v {
			out = append(out, targets(child)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, targets(child)...)
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
