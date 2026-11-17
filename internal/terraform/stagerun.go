package terraform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

// stageRun holds what the stage commands share: the config, the terraform
// service of the target environment and the command's name for messages.
type stageRun struct {
	cfg  *StagesConfig
	s    *service
	rt   *app.Runtime
	name string
}

func newStageRun(rt *app.Runtime, name, configPath string) (*stageRun, error) {
	cfg, err := loadStagesConfig(configPath)
	if err != nil {
		return nil, cmdError(rt.Stderr, name, "%v", err)
	}
	return &stageRun{cfg: cfg, s: newService(rt), rt: rt, name: name}, nil
}

// fail prints "mars <cmd>: <msg>" and returns exit code 1.
func (r *stageRun) fail(format string, args ...any) error {
	return cmdError(r.rt.Stderr, r.name, format, args...)
}

func (r *stageRun) logf(format string, args ...any) {
	fmt.Fprintf(r.s.stderr, format+"\n", args...)
}

// stageError says which step of which stage failed. A terraform failure has
// already printed terraform's own message.
func stageError(st *StageEntry, step string, err error) error {
	var exitErr *runner.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("stage %s: %s failed (exit %d)", st.Name, step, exitErr.Code)
	}
	return fmt.Errorf("stage %s: %s: %w", st.Name, step, err)
}

// inStage runs the stage's setup hook, then enterStage.
func (r *stageRun) inStage(ctx context.Context, st *StageEntry, fn func() error) error {
	if err := r.runSetup(ctx, st); err != nil {
		return err
	}
	return r.enterStage(ctx, st, fn)
}

// runSetup runs the stage's setup hook, if any, in the project root.
func (r *stageRun) runSetup(ctx context.Context, st *StageEntry) error {
	setup := r.cfg.setupFor(st)
	if len(setup) == 0 {
		return nil
	}
	cmd := runner.Command{
		Argv: setup,
		Dir:  r.cfg.root,
		Env: map[string]string{
			"MARS_ENV":       r.s.env,
			"MARS_STAGE":     st.Name,
			"MARS_STAGE_DIR": r.cfg.stageDir(st),
		},
	}
	if err := r.s.sequence(ctx, cmd); err != nil {
		return stageError(st, "setup", err)
	}
	return nil
}

// enterStage, inside the stage directory, installs the stage's terraform,
// checks the environment, runs terraform init, selects the workspace and
// calls fn. The working directory is restored after.
func (r *stageRun) enterStage(ctx context.Context, st *StageEntry, fn func() error) (err error) {
	dir := r.cfg.stageDir(st)
	if fi, statErr := os.Stat(dir); statErr != nil || !fi.IsDir() {
		return fmt.Errorf("stage %s: no directory %s", st.Name, dir)
	}
	prev, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	defer func() {
		if cdErr := os.Chdir(prev); cdErr != nil && err == nil {
			err = cdErr
		}
	}()
	if err := r.s.beforeWorkspaceCommand(ctx); err != nil {
		return stageError(st, "environment check", err)
	}
	initCmd := runner.Cmd("terraform", append([]string{"init"}, r.cfg.initArgs()...)...)
	if err := r.s.sequence(ctx, initCmd); err != nil {
		return stageError(st, "terraform init", err)
	}
	if err := r.s.sequence(ctx, r.s.workspaceSelect()); err != nil {
		return stageError(st, "terraform workspace select", err)
	}
	return fn()
}

// plan saves a plan of the stage to planFile, in the stage directory. The
// -out flag comes last, so it wins over a -out in TF_CLI_ARGS_plan.
func (r *stageRun) plan(ctx context.Context, st *StageEntry, planFile string, refreshOnly bool) error {
	args := append([]string{"plan"}, r.s.varFileArgs()...)
	args = append(args, "-input=false")
	if refreshOnly {
		args = append(args, "-refresh-only")
	}
	args = append(args, "-out="+planFile)
	if err := r.s.sequence(ctx, runner.Cmd("terraform", args...)); err != nil {
		return stageError(st, "terraform plan", err)
	}
	return nil
}

// capture runs a terraform command and returns its stdout; its stderr
// passes through.
func (r *stageRun) capture(ctx context.Context, st *StageEntry, args ...string) ([]byte, error) {
	cmd := runner.Command{Argv: append([]string{"terraform"}, args...), Stderr: r.s.stderr, Quiet: true}
	fmt.Fprintf(r.s.stderr, "(%s)\n", runner.ShellCommand(cmd.Argv))
	out, err := r.s.runner.Capture(ctx, cmd)
	if err != nil {
		return nil, stageError(st, "terraform "+strings.Join(args[:min(len(args), 2)], " "), err)
	}
	return out, nil
}

// showJSON is terraform show -json of a saved plan; it checks the output is
// a plan.
func (r *stageRun) showJSON(ctx context.Context, st *StageEntry, planFile string) ([]byte, *planDoc, error) {
	raw, err := r.capture(ctx, st, "show", "-json", planFile)
	if err != nil {
		return nil, nil, err
	}
	plan, err := parsePlan(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("stage %s: %w", st.Name, err)
	}
	return raw, plan, nil
}

// stateIsEmpty reports whether terraform state list lists no resource.
func (r *stageRun) stateIsEmpty(ctx context.Context, st *StageEntry) (bool, error) {
	out, err := r.capture(ctx, st, "state", "list")
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) == 0, nil
}

// applyPlan applies a saved plan. env adds variables for the apply.
func (r *stageRun) applyPlan(ctx context.Context, st *StageEntry, planFile string, env map[string]string) error {
	cmd := runner.Cmd("terraform", "apply", "-input=false", planFile)
	cmd.Env = env
	if err := r.s.sequence(ctx, cmd); err != nil {
		return stageError(st, "terraform apply", err)
	}
	return nil
}

// writeFile writes b to path, making its directory. An empty path writes
// nothing.
func writeReportFile(path string, b []byte) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: the reports directory other steps read
		return err
	}
	return os.WriteFile(path, b, 0o644) //nolint:gosec // G306: a report other steps read
}

// writeJSON writes v as indented JSON, as writeReport does.
func writeReportJSON(path string, v any) error {
	if path == "" {
		return nil
	}
	b, err := indentedJSON(v)
	if err != nil {
		return err
	}
	return writeReportFile(path, b)
}

func indentedJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := jsonEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// copyFile copies src to dst; an empty dst copies nothing.
func copyFile(src, dst string) error {
	if dst == "" {
		return nil
	}
	b, err := os.ReadFile(src) //nolint:gosec // G304: a report this command wrote
	if err != nil {
		return err
	}
	return writeReportFile(dst, b)
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

// removeGlob removes every file the glob matches; an empty glob removes
// nothing.
func removeGlob(glob string) error {
	if glob == "" {
		return nil
	}
	matches, err := filepath.Glob(glob)
	if err != nil {
		return err
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func removeFile(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// driftDoc is a drift report with the config's stamps, as written: the
// report's fields, the stamps, then resources.
func (r *stageRun) driftDoc(rep *DriftReport) orderedObject {
	doc := orderedObject{
		{"drift_detected", rep.DriftDetected},
		{"drift_count", rep.DriftCount},
		{"actionable", rep.Actionable},
		{"actionable_count", rep.ActionableCount},
		{"apply_skipped", rep.ApplySkipped},
	}
	for _, p := range r.cfg.Drift.StampEnv {
		var v any
		if s := os.Getenv(p.value); s != "" {
			v = s
		}
		doc = append(doc, jsonField{p.key, v})
	}
	resources := rep.Resources
	if resources == nil {
		resources = []map[string]any{}
	}
	return append(doc, jsonField{"resources", resources})
}

// writeDrift writes the stage's drift report and its alias.
func (r *stageRun) writeDrift(st *StageEntry, rep *DriftReport, perStage bool) error {
	doc := r.driftDoc(rep)
	if perStage {
		if err := writeReportJSON(r.cfg.outPath(r.cfg.driftName(), st.Name), doc); err != nil {
			return err
		}
	}
	return writeReportJSON(r.cfg.outPath(r.cfg.driftAliasName(), st.Name), doc)
}

// stripProviders deletes every .terraform/providers directory under root.
// The deletes go through an os.Root, so a symlink cannot lead them out of
// the project.
func stripProviders(root string, log func(string, ...any)) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	var dirs []string
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable directory is skipped, as find does
		}
		if !d.IsDir() || d.Name() != "providers" || filepath.Base(filepath.Dir(path)) != ".terraform" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		dirs = append(dirs, rel)
		return filepath.SkipDir
	})
	if walkErr != nil {
		return walkErr
	}
	for _, rel := range dirs {
		if err := r.RemoveAll(rel); err != nil {
			return err
		}
		log("  stripped %s", rel)
	}
	return nil
}

// jsonEncoder writes indented JSON without HTML escaping.
func jsonEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc
}
