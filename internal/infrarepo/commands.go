package infrarepo

import (
	"context"
	"os"
	"path/filepath"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

// The infra-* commands. They read no mars config and take no part in the
// Terraform commands: a project opts in by calling them (the template's
// shell_utils.sh does). The <env> argument mars requires before every
// command is not used.
//
// The project root is --root, else $MARS_PROJECT_ROOT (the template exports
// it), else the current directory.

// RootFlag is the project root of every infra-* command.
type RootFlag struct {
	Root string `name:"root" env:"MARS_PROJECT_ROOT" default:"." help:"Project root (default $MARS_PROJECT_ROOT, else the current directory)."`
}

func (f RootFlag) repo(rt *app.Runtime) (*Repo, error) {
	root, err := filepath.Abs(f.Root)
	if err != nil {
		return nil, err
	}
	stdout, stderr := rt.Stdout, rt.Stderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return &Repo{Root: root, Runner: rt.Runner, Stdout: stdout, Stderr: stderr}, nil
}

// CloneCmd is `mars <env> infra-clone`: give a tree without .git the -infra
// repo's git state. Exit 0 always (it warns instead).
type CloneCmd struct {
	RootFlag
}

func (c *CloneCmd) Run(ctx context.Context, rt *app.Runtime) error {
	r, err := c.repo(rt)
	if err != nil {
		return err
	}
	r.Clone(ctx)
	return nil
}

// MergeCmd is `mars <env> infra-merge [--abort-on-conflict]`. Exit 0
// merged (or nothing to do), 1 refused or failed, 2 not merged on purpose
// (see Repo.Merge).
type MergeCmd struct {
	RootFlag
	AbortOnConflict bool `name:"abort-on-conflict" help:"Abort a merge that fails with conflicts (apply), so no conflict is left for the snapshot."`
}

func (c *MergeCmd) Run(ctx context.Context, rt *app.Runtime) error {
	r, err := c.repo(rt)
	if err != nil {
		return err
	}
	return runner.ExitSilently(r.Merge(ctx, c.AbortOnConflict))
}

// PullCmd is `mars <env> infra-pull`: merge origin/main (when there is an
// origin remote), then infra/main. Exit 0 or the failing step's code.
type PullCmd struct {
	RootFlag
}

func (c *PullCmd) Run(ctx context.Context, rt *app.Runtime) error {
	r, err := c.repo(rt)
	if err != nil {
		return err
	}
	return runner.ExitSilently(r.Pull(ctx))
}

// CommitCmd is `mars <env> infra-commit [message]`. Exit 0 committed (or
// nothing to commit), 1 refused or failed with nothing committed.
type CommitCmd struct {
	RootFlag
	Message string `arg:"" optional:"" help:"Commit message."`
}

func (c *CommitCmd) Run(ctx context.Context, rt *app.Runtime) error {
	r, err := c.repo(rt)
	if err != nil {
		return err
	}
	return runner.ExitSilently(r.Commit(ctx, c.Message))
}

// SnapshotCmd is `mars <env> infra-snapshot [message]`: commit the deployed
// tree on top of infra/main. Exit 0 or 1.
type SnapshotCmd struct {
	RootFlag
	Message string `arg:"" optional:"" help:"Commit message."`
}

func (c *SnapshotCmd) Run(ctx context.Context, rt *app.Runtime) error {
	r, err := c.repo(rt)
	if err != nil {
		return err
	}
	return runner.ExitSilently(r.Snapshot(ctx, c.Message))
}

// PushCmd is `mars <env> infra-push`. Exit 0 always (it warns instead).
type PushCmd struct {
	RootFlag
}

func (c *PushCmd) Run(ctx context.Context, rt *app.Runtime) error {
	r, err := c.repo(rt)
	if err != nil {
		return err
	}
	r.Push(ctx)
	return nil
}

// PersistCmd is `mars <env> infra-persist`: clone if needed, snapshot,
// push. Exit 0 always (it warns instead).
type PersistCmd struct {
	RootFlag
}

func (c *PersistCmd) Run(ctx context.Context, rt *app.Runtime) error {
	r, err := c.repo(rt)
	if err != nil {
		return err
	}
	r.Persist(ctx)
	return nil
}
