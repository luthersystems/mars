package main

import (
	"context"
	"os"

	"github.com/luthersystems/mars/internal/cli"
	"github.com/luthersystems/mars/internal/runner"
)

func main() {
	r := &runner.Real{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	os.Exit(cli.Main(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr, r))
}
