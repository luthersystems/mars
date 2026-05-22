package app

import (
	"io"

	"github.com/luthersystems/mars/internal/runner"
)

type Runtime struct {
	Target     string
	Verbosity  int
	SkipPrompt bool
	Runner     runner.Runner
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Clock      Clock
}

type Clock interface {
	Unix() int64
}
