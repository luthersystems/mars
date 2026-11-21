package terraform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/luthersystems/insideout-terraform-presets/pkg/planredact"
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
	s := newService(rt)
	s.shareInput()
	return &stageRun{cfg: cfg, s: s, rt: rt, name: name}, nil
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

// copyPlanJSON copies a stage's plan JSON to the alias through
// planredact.JSON. This run wrote src redacted, but with --continue src may
// be a file an older mars wrote with the credential tfvars in clear
// (luthersystems/reliable#2553). Redaction is idempotent, so a redacted src
// comes out unchanged. A src that cannot be redacted is an error: the alias
// is uploaded, so it is never a raw copy.
func copyPlanJSON(src, dst string) error {
	b, err := os.ReadFile(src) //nolint:gosec // G304: a plan JSON this command's config names
	if err != nil {
		return err
	}
	redacted, err := planredact.JSON(b)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	return writeReportFile(dst, redacted)
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
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

// driftDoc is a drift report as written: the DriftReport's own fields in
// its order, with the config's stamps inserted before "resources".
func (r *stageRun) driftDoc(rep *DriftReport) (orderedObject, error) {
	cp := *rep
	if cp.Resources == nil {
		cp.Resources = []map[string]any{}
	}
	b, err := encodeJSON(cp, false)
	if err != nil {
		return nil, err
	}
	fields, err := objectFields(b)
	if err != nil {
		return nil, err
	}
	stamps := make(orderedObject, 0, len(r.cfg.Drift.StampEnv))
	for _, p := range r.cfg.Drift.StampEnv {
		var v any
		if s := os.Getenv(p.value); s != "" {
			v = s
		}
		stamps = append(stamps, jsonField{p.key, v})
	}
	doc := make(orderedObject, 0, len(fields)+len(stamps))
	inserted := false
	for _, f := range fields {
		if f.Key == "resources" && !inserted {
			doc = append(doc, stamps...)
			inserted = true
		}
		doc = append(doc, f)
	}
	if !inserted {
		doc = append(doc, stamps...)
	}
	return doc, nil
}

// writeDrift builds the drift document once and writes it to each path
// ("" is skipped).
func (r *stageRun) writeDrift(rep *DriftReport, paths ...string) error {
	doc, err := r.driftDoc(rep)
	if err != nil {
		return err
	}
	for _, p := range paths {
		if err := writeReportJSON(p, doc); err != nil {
			return err
		}
	}
	return nil
}

// combineDrift is one drift report of several stages' reports: the counts
// summed, the resources in stage order, and apply_skipped when any stage's
// is.
func combineDrift(reps []*DriftReport) *DriftReport {
	out := &DriftReport{Resources: []map[string]any{}}
	for _, rep := range reps {
		out.DriftCount += rep.DriftCount
		out.ActionableCount += rep.ActionableCount
		out.ApplySkipped = out.ApplySkipped || rep.ApplySkipped
		out.Resources = append(out.Resources, rep.Resources...)
	}
	out.DriftDetected = out.DriftCount > 0
	out.Actionable = out.ActionableCount > 0
	return out
}

// readDriftReport reads a drift report a run wrote, keeping number
// literals as they are. The config's stamps are dropped (they are written
// again).
func readDriftReport(path string) (*DriftReport, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: a drift report the config names
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var rep DriftReport
	if err := dec.Decode(&rep); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &rep, nil
}

// removeAll removes every path, going on after a failure; the failures are
// joined to err.
func removeAll(err error, paths ...string) error {
	for _, p := range paths {
		if rmErr := removeFile(p); rmErr != nil {
			err = errors.Join(err, rmErr)
		}
	}
	return err
}

// removeAllInRoot removes rel under root through an os.Root, so a symlink
// on the way cannot lead the delete out of root.
func removeAllInRoot(root, rel string) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return r.RemoveAll(rel)
}

// stripProviders deletes every .terraform/providers directory under root.
// The deletes go through an os.Root, so a symlink cannot lead them out of
// the project.
func stripProviders(root string, log func(string, ...any)) error {
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
		if err := removeAllInRoot(root, rel); err != nil {
			return err
		}
		log("  stripped %s", rel)
	}
	return nil
}
