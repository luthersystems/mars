package packer

import (
	"context"
	"fmt"
	"os"

	"github.com/luthersystems/mars/internal/app"
	"github.com/luthersystems/mars/internal/runner"
)

type ValidateCmd struct{}

type BuildCmd struct {
	Debug bool `name:"debug"`
}

func (c *ValidateCmd) Run(ctx context.Context, rt *app.Runtime) error {
	return run(ctx, rt, []string{"packer", "validate", "packer.json"})
}

func (c *BuildCmd) Run(ctx context.Context, rt *app.Runtime) error {
	cmd := []string{"packer", "build"}
	if c.Debug {
		cmd = append(cmd, "-debug")
	}
	cmd = append(cmd, "packer.json")
	return run(ctx, rt, cmd)
}

func run(ctx context.Context, rt *app.Runtime, argv []string) error {
	stderr := rt.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	fmt.Fprintln(stderr, runner.ShellCommand(argv))
	return rt.Runner.Run(ctx, runner.Command{
		Argv:   argv,
		Dir:    rt.Target,
		Stdin:  rt.Stdin,
		Stdout: rt.Stdout,
		Stderr: stderr,
		Quiet:  true,
	})
}
