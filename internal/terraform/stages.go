package terraform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultStagesConfig is the stages config file mars reads when --config is
// not given, in the current directory. See docs/stages.md.
const DefaultStagesConfig = "mars-stages.yaml"

// StagesConfig is a project's mars-stages.yaml: its Terraform stages, in
// order, and the names of the reports plan-all, plan-stage and apply-stage
// write. mars knows nothing of a project beyond it.
type StagesConfig struct {
	Version         int          `yaml:"version"`
	OutputsDir      string       `yaml:"outputs_dir"`
	PlanFile        string       `yaml:"plan_file"`
	InitArgs        *[]string    `yaml:"init_args"`
	ContinueOnError *bool        `yaml:"continue_on_error"`
	StripProviders  bool         `yaml:"strip_providers"`
	Setup           []string     `yaml:"setup"`
	Files           StageFiles   `yaml:"files"`
	Drift           StagesDrift  `yaml:"drift"`
	Stages          []StageEntry `yaml:"stages"`

	// root is the directory of the config file; every path is relative to
	// it. Absolute.
	root string
}

// StageFiles are the report file names, inside the outputs directory. A nil
// value takes the default; an empty one turns the file off. {stage} is the
// stage's name.
type StageFiles struct {
	PlanJSON     *string `yaml:"plan_json"`
	PlanAlias    *string `yaml:"plan_alias"`
	StageSummary *string `yaml:"stage_summary"`
	Summary      *string `yaml:"summary"`
	Drift        *string `yaml:"drift"`
	DriftAlias   *string `yaml:"drift_alias"`
	Outputs      *string `yaml:"outputs"`
	OutputsAlias *string `yaml:"outputs_alias"`
}

// StagesDrift configures the drift reports.
type StagesDrift struct {
	// StampEnv adds a field to every drift report per entry, from an
	// environment variable, in config order.
	StampEnv orderedStrings `yaml:"stamp_env"`
}

// StageEntry is one stage.
type StageEntry struct {
	Name               string `yaml:"name"`
	Dir                string `yaml:"dir"`
	ApplyIfNoState     bool   `yaml:"apply_if_no_state"`
	PlanAliasPreferred bool   `yaml:"plan_alias_preferred"`
	// Setup is the stage's own setup hook: absent (nil) uses the
	// config's, an empty list runs none.
	Setup *[]string `yaml:"setup"`
}

// orderedStrings is a YAML mapping of strings that keeps its order.
type orderedStrings []stringPair

type stringPair struct{ key, value string }

func (o *orderedStrings) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: want a mapping of field to environment variable", n.Line)
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode || v.Kind != yaml.ScalarNode {
			return fmt.Errorf("line %d: want a string to string mapping", k.Line)
		}
		if seen[k.Value] {
			return fmt.Errorf("line %d: duplicate field %q", k.Line, k.Value)
		}
		seen[k.Value] = true
		*o = append(*o, stringPair{k.Value, v.Value})
	}
	return nil
}

var (
	stageNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	envNameRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// driftReportFieldSet is the set of DriftReport's JSON fields, read from
// the struct itself so it cannot fall out of step with it. A stamp may not
// use one.
func driftReportFieldSet() map[string]bool {
	b, err := encodeJSON(DriftReport{}, false)
	if err != nil {
		panic(err) // DriftReport always encodes
	}
	fields, err := objectFields(b)
	if err != nil {
		panic(err)
	}
	set := map[string]bool{}
	for _, f := range fields {
		set[f.Key] = true
	}
	return set
}

// objectFields reads a JSON object's top-level fields in order, each value
// raw.
func objectFields(b []byte) (orderedObject, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var out orderedObject
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, jsonField{key, raw})
	}
	return out, nil
}

func strDefault(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func (c *StagesConfig) outputsDir() string {
	d := c.OutputsDir
	if d == "" {
		d = "outputs"
	}
	if filepath.IsAbs(d) {
		return filepath.Clean(d)
	}
	return filepath.Join(c.root, d)
}

func (c *StagesConfig) planFile() string {
	if c.PlanFile == "" {
		return "mars.tfplan"
	}
	return c.PlanFile
}

func (c *StagesConfig) initArgs() []string {
	if c.InitArgs == nil {
		return []string{"-input=false", "-reconfigure"}
	}
	return *c.InitArgs
}

func (c *StagesConfig) continueOnError() bool {
	return c.ContinueOnError == nil || *c.ContinueOnError
}

// The report names, before {stage} is expanded.
func (c *StagesConfig) planJSONName() string {
	return strDefault(c.Files.PlanJSON, "tfplan-{stage}.json")
}
func (c *StagesConfig) planAliasName() string {
	return strDefault(c.Files.PlanAlias, "tfplan.json")
}
func (c *StagesConfig) stageSummaryName() string { return strDefault(c.Files.StageSummary, "") }
func (c *StagesConfig) summaryName() string {
	return strDefault(c.Files.Summary, "plan-summary.json")
}
func (c *StagesConfig) driftName() string      { return strDefault(c.Files.Drift, "drift-{stage}.json") }
func (c *StagesConfig) driftAliasName() string { return strDefault(c.Files.DriftAlias, "drift.json") }
func (c *StagesConfig) outputsName() string    { return strDefault(c.Files.Outputs, "{stage}.json") }
func (c *StagesConfig) outputsAliasName() string {
	return strDefault(c.Files.OutputsAlias, "outputs.json")
}

// outPath is the path of a report: name with {stage} expanded, inside the
// outputs directory, or "" when the report is off.
func (c *StagesConfig) outPath(name, stage string) string {
	if name == "" {
		return ""
	}
	return filepath.Join(c.outputsDir(), strings.ReplaceAll(name, "{stage}", stage))
}

// stageDir is the stage's directory, absolute.
func (c *StagesConfig) stageDir(st *StageEntry) string {
	d := st.Dir
	if d == "" {
		d = st.Name
	}
	return filepath.Join(c.root, d)
}

func (c *StagesConfig) setupFor(st *StageEntry) []string {
	if st.Setup != nil {
		return *st.Setup
	}
	return c.Setup
}

func (c *StagesConfig) stage(name string) *StageEntry {
	for i := range c.Stages {
		if c.Stages[i].Name == name {
			return &c.Stages[i]
		}
	}
	return nil
}

// selectStages returns the named stages in the given order, or every stage
// when names is empty.
func (c *StagesConfig) selectStages(names []string) ([]*StageEntry, error) {
	if len(names) == 0 {
		out := make([]*StageEntry, 0, len(c.Stages))
		for i := range c.Stages {
			out = append(out, &c.Stages[i])
		}
		return out, nil
	}
	seen := map[string]bool{}
	out := make([]*StageEntry, 0, len(names))
	for _, n := range names {
		st := c.stage(n)
		if st == nil {
			return nil, fmt.Errorf("no stage %q in the stages config", n)
		}
		if seen[n] {
			return nil, fmt.Errorf("stage %q named twice", n)
		}
		seen[n] = true
		out = append(out, st)
	}
	return out, nil
}

// loadStagesConfig reads and checks the config file at path.
func loadStagesConfig(path string) (*StagesConfig, error) {
	if path == "" {
		path = DefaultStagesConfig
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the config file the caller named
	if err != nil {
		return nil, fmt.Errorf("read stages config: %w", err)
	}
	var c StagesConfig
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: empty stages config", path)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.root = filepath.Dir(abs)
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *StagesConfig) validate() error {
	if c.Version != 1 {
		return fmt.Errorf("version must be 1, not %d", c.Version)
	}
	if len(c.Stages) == 0 {
		return errors.New("no stages")
	}
	if c.OutputsDir != "" && !filepath.IsAbs(c.OutputsDir) && !filepath.IsLocal(c.OutputsDir) {
		return fmt.Errorf("outputs_dir %q leaves the project root", c.OutputsDir)
	}
	if !isPlainLocal(c.planFile()) {
		return fmt.Errorf("plan_file %q must be a file name inside the stage directory", c.PlanFile)
	}
	perStage := map[string]string{
		"plan_json": c.planJSONName(), "stage_summary": c.stageSummaryName(),
		"drift": c.driftName(), "outputs": c.outputsName(),
	}
	for key, name := range perStage {
		if name == "" {
			continue
		}
		if !strings.Contains(name, "{stage}") {
			return fmt.Errorf("files.%s %q must contain {stage}", key, name)
		}
		if !isPlainLocal(strings.ReplaceAll(name, "{stage}", "x")) {
			return fmt.Errorf("files.%s %q must be a path inside outputs_dir, without glob characters", key, name)
		}
	}
	single := map[string]string{
		"plan_alias": c.planAliasName(), "summary": c.summaryName(),
		"drift_alias": c.driftAliasName(), "outputs_alias": c.outputsAliasName(),
	}
	for key, name := range single {
		if name == "" {
			continue
		}
		if strings.Contains(name, "{stage}") || !isPlainLocal(name) {
			return fmt.Errorf("files.%s %q must be a path inside outputs_dir, without {stage} or glob characters", key, name)
		}
	}
	reportFields := driftReportFieldSet()
	for _, p := range c.Drift.StampEnv {
		if reportFields[p.key] || p.key == "" {
			return fmt.Errorf("drift.stamp_env: field %q is not allowed", p.key)
		}
		if !envNameRE.MatchString(p.value) {
			return fmt.Errorf("drift.stamp_env.%s: %q is not an environment variable name", p.key, p.value)
		}
	}
	names := map[string]bool{}
	preferred := ""
	for i := range c.Stages {
		st := &c.Stages[i]
		if !stageNameRE.MatchString(st.Name) {
			return fmt.Errorf("stages[%d]: name %q must match %s", i, st.Name, stageNameRE)
		}
		if names[st.Name] {
			return fmt.Errorf("stages[%d]: duplicate name %q", i, st.Name)
		}
		names[st.Name] = true
		if st.Dir != "" && !filepath.IsLocal(st.Dir) {
			return fmt.Errorf("stage %s: dir %q leaves the project root", st.Name, st.Dir)
		}
		if st.PlanAliasPreferred {
			if preferred != "" {
				return fmt.Errorf("stages %s and %s are both plan_alias_preferred", preferred, st.Name)
			}
			preferred = st.Name
		}
	}
	return c.checkReportNames()
}

// checkReportNames rejects two reports with the same file: two kinds of
// report, or two stages, or a stage's report and a global one. Each kind
// would overwrite (or plan-all's cleanup delete) the other.
func (c *StagesConfig) checkReportNames() error {
	owner := map[string]string{}
	claim := func(path, what string) error {
		if path == "" {
			return nil
		}
		if prev, ok := owner[path]; ok {
			return fmt.Errorf("files: %s and %s are both %s", prev, what, path)
		}
		owner[path] = what
		return nil
	}
	global := []struct{ key, name string }{
		{"plan_alias", c.planAliasName()}, {"summary", c.summaryName()},
		{"drift_alias", c.driftAliasName()}, {"outputs_alias", c.outputsAliasName()},
	}
	for _, g := range global {
		if err := claim(c.outPath(g.name, ""), "files."+g.key); err != nil {
			return err
		}
	}
	perStage := []struct{ key, name string }{
		{"plan_json", c.planJSONName()}, {"stage_summary", c.stageSummaryName()},
		{"drift", c.driftName()}, {"outputs", c.outputsName()},
	}
	for i := range c.Stages {
		for _, p := range perStage {
			what := fmt.Sprintf("files.%s of stage %s", p.key, c.Stages[i].Name)
			if err := claim(c.outPath(p.name, c.Stages[i].Name), what); err != nil {
				return err
			}
		}
	}
	return nil
}

// isPlainLocal is a relative path that stays below its base and has no glob
// characters, so a glob built from it matches only what it names.
func isPlainLocal(p string) bool {
	return p != "" && filepath.IsLocal(p) && !strings.ContainsAny(p, `*?[\`)
}

// jsonField is one field of an orderedObject.
type jsonField struct {
	Key   string
	Value any
}

// orderedObject is a JSON object written with its fields in order.
type orderedObject []jsonField

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, err := marshalNoEscape(f.Key)
		if err != nil {
			return nil, err
		}
		v, err := marshalNoEscape(f.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// marshalNoEscape is compact JSON without HTML escaping, as reports are
// written.
func marshalNoEscape(v any) ([]byte, error) { return encodeJSON(v, false) }
