package terraform

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

const tfenvInstallLockPath = "/opt/tfenv/versions/.install.lock"

type InitCmd struct {
	BackendConfig []string `name:"backend-config" help:"A backend config file or key=value assignment."`
	Upgrade       bool     `name:"upgrade" help:"Upgrade modules and plugins."`
	Reconfigure   bool     `name:"reconfigure" help:"Reconfigure the backend on init."`
}

type NewWorkspaceCmd struct{}

type RawCmd struct {
	Args []string `arg:"" optional:"" passthrough:"" name:"args"`
}

type PlanCmd struct {
	Destroy     bool     `name:"destroy"`
	Out         string   `name:"out"`
	Target      []string `name:"target"`
	ApplyPlan   bool     `name:"apply"`
	RefreshOnly bool     `name:"refresh-only" xor:"refresh"`
	SkipRefresh bool     `name:"skip-refresh" xor:"refresh"`
}

type ApplyCmd struct {
	Plan        string `name:"plan"`
	Target      string `name:"target"`
	Approve     bool   `name:"approve"`
	RefreshOnly bool   `name:"refresh-only"`
}

type DestroyCmd struct {
	Approve bool `name:"approve"`
}

type ShowCmd struct {
	Plan string `name:"plan"`
}

type GraphCmd struct {
	DrawCycles bool `name:"draw-cycles"`
}

type ImportCmd struct {
	AllowMissingConfig bool   `name:"allow-missing-config" help:"Allow import when no resource configuration block exists."`
	Addr               string `arg:"" help:"Address to import resource to."`
	ResourceID         string `arg:"" name:"resource_id" help:"Resource-specific ID."`
}

type TaintCmd struct {
	Module string   `name:"module" help:"Module containing the resource to taint."`
	Name   []string `arg:"" name:"name" help:"A resource to taint."`
}

type UntaintCmd struct {
	Module string   `name:"module" help:"Module containing the resource to untaint."`
	Name   []string `arg:"" name:"name" help:"A resource to untaint."`
}

type MigrationFromPlanCmd struct {
	TFPlanFile    string `arg:"" name:"tfplan_file" help:"Input from terraform plan -out."`
	MigrationFile string `arg:"" name:"migration_file" help:"Output HCL migration file."`
}

type MigratePlanCmd struct {
	MigrationFile string `arg:"" name:"migration_file" help:"HCL migration file."`
}

type MigrateApplyCmd struct {
	MigrationFile string `arg:"" name:"migration_file" help:"HCL migration file."`
}

type service struct {
	env        string
	verbosity  int
	skipPrompt bool
	runner     runner.Runner
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	clock      app.Clock
}

func (c *InitCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.tfenvInit(ctx); err != nil {
		return err
	}
	args := []string{"terraform", "init"}
	for _, config := range c.BackendConfig {
		args = append(args, "-backend-config", config)
	}
	if c.Upgrade {
		args = append(args, "--upgrade")
	}
	if c.Reconfigure {
		args = append(args, "--reconfigure")
	}
	return s.sequence(ctx, runner.Cmd(args[0], args[1:]...))
}

func (c *NewWorkspaceCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.tfenvInit(ctx); err != nil {
		return err
	}
	return s.sequence(ctx, runner.Cmd("terraform", "workspace", "new", s.env))
}

func (c *RawCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.beforeWorkspaceCommand(ctx); err != nil {
		return err
	}
	if len(c.Args) > 0 && c.Args[0] == "--" {
		c.Args = c.Args[1:]
	}
	cmd := append([]string{"terraform"}, c.Args...)
	return s.sequence(ctx, s.workspaceSelect(), runner.Cmd(cmd[0], cmd[1:]...))
}

func (c *PlanCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.beforeWorkspaceCommand(ctx); err != nil {
		return err
	}
	planPath := c.Out
	if c.ApplyPlan && planPath == "" {
		if err := os.MkdirAll(".tf-plans", 0o755); err != nil {
			return err
		}
		clock := s.clock
		if clock == nil {
			clock = realClock{}
		}
		file, err := os.CreateTemp(".tf-plans", fmt.Sprintf("tf-plan-%s-%d-*.out", s.env, clock.Unix()))
		if err != nil {
			return err
		}
		planPath = file.Name()
		if err := file.Close(); err != nil {
			return err
		}
	}

	args := append([]string{"terraform", "plan"}, s.varFileArgs()...)
	if c.Destroy {
		args = append(args, "-destroy")
	}
	if planPath != "" {
		args = append(args, "-out="+planPath)
	}
	for _, target := range c.Target {
		args = append(args, "-target", target)
	}
	if c.RefreshOnly {
		args = append(args, "-refresh-only")
	}
	if c.SkipRefresh {
		args = append(args, "-refresh=false")
	}

	if err := s.sequence(ctx, s.workspaceSelect(), runner.Cmd(args[0], args[1:]...)); err != nil {
		if c.ApplyPlan {
			fmt.Fprintln(s.stdout, "Aborted -- planning failed")
		}
		return err
	}
	if !c.ApplyPlan {
		return nil
	}
	if !s.confirmApply(planPath) {
		return runner.Exit(1)
	}
	return (&ApplyCmd{Plan: planPath}).Run(ctx, rt)
}

func (c *ApplyCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.beforeWorkspaceCommand(ctx); err != nil {
		return err
	}
	var args []string
	if c.Plan != "" {
		args = []string{c.Plan}
	} else {
		args = s.varFileArgs()
	}
	if c.Target != "" {
		args = append(args, "-target", c.Target)
	}
	if c.RefreshOnly {
		args = append(args, "-refresh-only")
	}
	if c.Approve {
		args = append(args, "-auto-approve")
	}
	return s.sequence(ctx, s.workspaceSelect(), runner.Cmd("terraform", append([]string{"apply"}, args...)...))
}

func (c *DestroyCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.beforeWorkspaceCommand(ctx); err != nil {
		return err
	}
	args := s.varFileArgs()
	if c.Approve {
		args = append(args, "-auto-approve")
	}
	return s.sequence(ctx, s.workspaceSelect(), runner.Cmd("terraform", append([]string{"destroy"}, args...)...))
}

func (c *ShowCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.beforeWorkspaceCommand(ctx); err != nil {
		return err
	}
	args := []string{"show"}
	if c.Plan != "" {
		args = append(args, c.Plan)
	}
	return s.sequence(ctx, s.workspaceSelect(), runner.Cmd("terraform", args...))
}

func (c *GraphCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.beforeWorkspaceCommand(ctx); err != nil {
		return err
	}
	args := []string{"graph"}
	if c.DrawCycles {
		args = append(args, "-draw-cycles")
	}
	return s.sequence(ctx, s.workspaceSelect(), runner.Cmd("terraform", args...))
}

func (c *ImportCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.beforeWorkspaceCommand(ctx); err != nil {
		return err
	}
	args := append([]string{"import"}, s.varFileArgs()...)
	if c.AllowMissingConfig {
		args = append(args, "-allow-missing-config")
	}
	args = append(args, c.Addr, c.ResourceID)
	return s.sequence(ctx, s.workspaceSelect(), runner.Cmd("terraform", args...))
}

func (c *TaintCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.beforeWorkspaceCommand(ctx); err != nil {
		return err
	}
	for _, name := range c.Name {
		args := []string{"taint"}
		if c.Module != "" {
			args = append(args, "-module", c.Module)
		}
		args = append(args, name)
		if err := s.sequence(ctx, s.workspaceSelect(), runner.Cmd("terraform", args...)); err != nil {
			return err
		}
	}
	return nil
}

func (c *UntaintCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.beforeWorkspaceCommand(ctx); err != nil {
		return err
	}
	for _, name := range c.Name {
		args := []string{"untaint"}
		if c.Module != "" {
			args = append(args, "-module", c.Module)
		}
		args = append(args, name)
		if err := s.sequence(ctx, s.workspaceSelect(), runner.Cmd("terraform", args...)); err != nil {
			return err
		}
	}
	return nil
}

func (c *MigrationFromPlanCmd) Run(ctx context.Context, rt *app.Runtime) error {
	s := newService(rt)
	if err := s.tfenvInit(ctx); err != nil {
		return err
	}
	return s.runner.Pipe(
		ctx,
		runner.Cmd("terraform", "show", "-json", c.TFPlanFile),
		runner.Cmd("tfedit", "migration", "fromplan", "-o="+c.MigrationFile),
	)
}

func (c *MigratePlanCmd) Run(ctx context.Context, rt *app.Runtime) error {
	return migrate(ctx, rt, "plan", c.MigrationFile, false)
}

func (c *MigrateApplyCmd) Run(ctx context.Context, rt *app.Runtime) error {
	return migrate(ctx, rt, "apply", c.MigrationFile, true)
}

func migrate(ctx context.Context, rt *app.Runtime, action string, migrationFile string, includeApply bool) error {
	s := newService(rt)
	if err := s.beforeWorkspaceCommand(ctx); err != nil {
		return err
	}
	env := map[string]string{
		"TFMIGRATE_LOG":      "DEBUG",
		"TF_CLI_ARGS_plan":   strings.Join(s.varFileArgs(), " "),
		"TF_CLI_ARGS_import": strings.Join(s.varFileArgs(), " "),
	}
	if includeApply {
		env["TF_CLI_ARGS_apply"] = strings.Join(s.varFileArgs(), " ")
	}
	if err := s.runner.Run(ctx, s.workspaceSelect()); err != nil {
		return err
	}
	return s.runner.Run(ctx, runner.Command{Argv: []string{"tfmigrate", action, migrationFile}, Env: env})
}

func newService(rt *app.Runtime) *service {
	stdin := rt.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}
	stdout := rt.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := rt.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	return &service{
		env:        rt.Target,
		verbosity:  rt.Verbosity,
		skipPrompt: rt.SkipPrompt,
		runner:     rt.Runner,
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
		clock:      rt.Clock,
	}
}

func (s *service) beforeWorkspaceCommand(ctx context.Context) error {
	if err := s.tfenvInit(ctx); err != nil {
		return err
	}
	if err := s.checkEnv(); err != nil {
		return err
	}
	return s.promptEnvSwitch(ctx)
}

func (s *service) tfenvInit(ctx context.Context) error {
	if _, err := os.Stat(".terraform-version"); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(s.stderr)
		fmt.Fprintln(s.stderr, "WARNING: .terraform-version not found -- using the default installed terraform")
		fmt.Fprintln(s.stderr)
		return nil
	} else if err != nil {
		return err
	}
	return s.sequence(ctx, tfenvInstallCmd())
}

func tfenvInstallCmd() runner.Command {
	return runner.Cmd("flock", tfenvInstallLockPath, "tfenv", "install")
}

func (s *service) checkEnv() error {
	if _, err := os.Stat(filepath.Join("vars", s.env)); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(s.stderr, "\nNo environment in this project: %s\n\n.", s.env)
		return runner.Exit(1)
	} else if err != nil {
		return err
	}
	return nil
}

func (s *service) promptEnvSwitch(ctx context.Context) error {
	out, err := s.runner.Capture(ctx, runner.Cmd("terraform", "workspace", "show"))
	if err != nil {
		return err
	}
	currEnv := strings.TrimSpace(string(out))
	if currEnv == s.env {
		return nil
	}
	fmt.Fprintf(s.stderr, "\nswitching environment \x1b[31m%s\x1b[0m ~> \x1b[32m%s\x1b[0m\n\n", currEnv, s.env)
	if s.skipPrompt {
		return nil
	}
	reader := bufio.NewReader(s.stdin)
	for {
		fmt.Fprintf(s.stderr, "switch to %s? [y/N] ", s.env)
		resp, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		resp = strings.ToLower(strings.TrimSpace(resp))
		if resp == "" {
			resp = "n"
		}
		if resp == "n" || resp == "no" {
			return runner.Exit(1)
		}
		if resp == "y" || resp == "yes" {
			return nil
		}
		fmt.Fprintln(s.stderr, "what?")
		if errors.Is(err, io.EOF) {
			return runner.Exit(1)
		}
	}
}

func (s *service) confirmApply(planPath string) bool {
	fmt.Fprintln(s.stdout)
	fmt.Fprintln(s.stdout, "Would you like to apply this plan?")
	reader := bufio.NewReader(s.stdin)
	cont := ""
	for cont != "yes" {
		fmt.Fprintln(s.stdout, "You must answer 'yes' to continue")
		fmt.Fprint(s.stdout, "> ")
		resp, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintln(s.stdout)
			fmt.Fprintln(s.stdout, "Aborted -- plan will not be applied")
			fmt.Fprintf(s.stdout, "path: %s\n", planPath)
			return false
		}
		if errors.Is(err, io.EOF) && resp == "" {
			fmt.Fprintln(s.stdout)
			cont = "no"
		} else {
			cont = strings.TrimSpace(resp)
		}
		if cont == "no" {
			fmt.Fprintln(s.stdout, "Aborted -- plan will not be applied")
			fmt.Fprintf(s.stdout, "path: %s\n", planPath)
			return false
		}
	}
	fmt.Fprintln(s.stdout)
	return true
}

func (s *service) varFileArgs() []string {
	var args []string
	for _, path := range s.locateVarFiles() {
		args = append(args, "-var-file="+path)
	}
	return args
}

func (s *service) locateVarFiles() []string {
	var paths []string
	for _, env := range []string{"common", s.env} {
		matches, _ := filepath.Glob(filepath.Join("vars", env, "*.tfvars"))
		sort.Strings(matches)
		for _, match := range matches {
			paths = append(paths, match)
		}
	}
	if s.verbosity >= 1 {
		for _, path := range paths {
			rel := strings.TrimPrefix(path, "vars"+string(filepath.Separator))
			fmt.Fprintf(s.stdout, "Variable file: %s\n", rel)
		}
	}
	return paths
}

func (s *service) workspaceSelect() runner.Command {
	return runner.Cmd("terraform", "workspace", "select", s.env)
}

func (s *service) sequence(ctx context.Context, cmds ...runner.Command) error {
	return runner.Sequence(ctx, s.runner, s.stderr, cmds...)
}

type realClock struct{}

func (realClock) Unix() int64 { return time.Now().Unix() }
