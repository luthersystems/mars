package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

type Command struct {
	Argv   []string
	Dir    string
	Env    map[string]string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Quiet  bool
}

type Runner interface {
	Run(ctx context.Context, cmd Command) error
	Capture(ctx context.Context, cmd Command) ([]byte, error)
	Pipe(ctx context.Context, producer Command, consumer Command) error
}

type Real struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("command exited with status %d", e.Code)
}

func (e *ExitError) Unwrap() error {
	return e.Err
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	var marsExit *ExitError
	if errors.As(err, &marsExit) {
		return marsExit.Code
	}
	return 1
}

func Exit(code int) error {
	if code == 0 {
		return nil
	}
	return &ExitError{Code: code}
}

func Cmd(name string, args ...string) Command {
	return Command{Argv: append([]string{name}, args...)}
}

func Sequence(ctx context.Context, r Runner, stderr io.Writer, cmds ...Command) error {
	if len(cmds) == 0 {
		return nil
	}
	fmt.Fprintf(stderr, "(%s)\n", ScriptString(cmds...))
	for _, cmd := range cmds {
		cmd.Quiet = true
		if err := r.Run(ctx, cmd); err != nil {
			return err
		}
	}
	return nil
}

func ScriptString(cmds ...Command) string {
	parts := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		parts = append(parts, ShellCommand(cmd.Argv))
	}
	return strings.Join(parts, " && ")
}

func ShellCommand(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, ShellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '=' || r == ',' ||
			(r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z'))
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func (r *Real) Run(ctx context.Context, cmd Command) error {
	if len(cmd.Argv) == 0 {
		return errors.New("empty command")
	}
	if !cmd.Quiet {
		fmt.Fprintln(r.stderr(cmd), ShellCommand(cmd.Argv))
	}
	execCmd := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	execCmd.Dir = cmd.Dir
	execCmd.Env = mergedEnv(cmd.Env)
	execCmd.Stdin = firstReader(cmd.Stdin, r.Stdin, os.Stdin)
	execCmd.Stdout = firstWriter(cmd.Stdout, r.Stdout, os.Stdout)
	execCmd.Stderr = r.stderr(cmd)
	return wrapExit(execCmd.Run())
}

func (r *Real) Capture(ctx context.Context, cmd Command) ([]byte, error) {
	if len(cmd.Argv) == 0 {
		return nil, errors.New("empty command")
	}
	if !cmd.Quiet {
		fmt.Fprintln(r.stderr(cmd), ShellCommand(cmd.Argv))
	}
	execCmd := exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	execCmd.Dir = cmd.Dir
	execCmd.Env = mergedEnv(cmd.Env)
	execCmd.Stdin = firstReader(cmd.Stdin, r.Stdin, os.Stdin)
	execCmd.Stderr = r.stderr(cmd)
	out, err := execCmd.Output()
	return out, wrapExit(err)
}

func (r *Real) Pipe(ctx context.Context, producer Command, consumer Command) error {
	if len(producer.Argv) == 0 || len(consumer.Argv) == 0 {
		return errors.New("empty command")
	}
	fmt.Fprintf(r.stderr(consumer), "%s | %s\n", ShellCommand(producer.Argv), ShellCommand(consumer.Argv))

	producerCmd := exec.CommandContext(ctx, producer.Argv[0], producer.Argv[1:]...)
	producerCmd.Dir = producer.Dir
	producerCmd.Env = mergedEnv(producer.Env)
	producerCmd.Stdin = firstReader(producer.Stdin, r.Stdin, os.Stdin)
	producerCmd.Stderr = r.stderr(producer)

	consumerCmd := exec.CommandContext(ctx, consumer.Argv[0], consumer.Argv[1:]...)
	consumerCmd.Dir = consumer.Dir
	consumerCmd.Env = mergedEnv(consumer.Env)
	consumerCmd.Stdout = firstWriter(consumer.Stdout, r.Stdout, os.Stdout)
	consumerCmd.Stderr = r.stderr(consumer)

	pipe, err := producerCmd.StdoutPipe()
	if err != nil {
		return err
	}
	consumerCmd.Stdin = pipe
	if err := producerCmd.Start(); err != nil {
		return wrapExit(err)
	}
	if err := consumerCmd.Start(); err != nil {
		_ = producerCmd.Wait()
		return wrapExit(err)
	}
	consumerErr := consumerCmd.Wait()
	producerErr := producerCmd.Wait()
	if consumerErr != nil {
		return wrapExit(consumerErr)
	}
	return wrapExit(producerErr)
}

func (r *Real) stderr(cmd Command) io.Writer {
	return firstWriter(cmd.Stderr, r.Stderr, os.Stderr)
}

func wrapExit(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &ExitError{Code: exitErr.ExitCode(), Err: err}
	}
	return err
}

func mergedEnv(extra map[string]string) []string {
	env := os.Environ()
	if len(extra) == 0 {
		return env
	}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+extra[key])
	}
	return env
}

func firstReader(readers ...io.Reader) io.Reader {
	for _, r := range readers {
		if r != nil {
			return r
		}
	}
	return nil
}

func firstWriter(writers ...io.Writer) io.Writer {
	for _, w := range writers {
		if w != nil {
			return w
		}
	}
	return io.Discard
}

type Recorded struct {
	Kind string
	Cmd  Command
}

type Fake struct {
	Records      []Recorded
	RunErrors    []error
	CaptureOut   [][]byte
	CaptureError []error
	PipeErrors   []error
}

func (f *Fake) Run(_ context.Context, cmd Command) error {
	f.Records = append(f.Records, Recorded{Kind: "run", Cmd: cmd})
	return shiftError(&f.RunErrors)
}

func (f *Fake) Capture(_ context.Context, cmd Command) ([]byte, error) {
	f.Records = append(f.Records, Recorded{Kind: "capture", Cmd: cmd})
	out := []byte{}
	if len(f.CaptureOut) > 0 {
		out = f.CaptureOut[0]
		f.CaptureOut = f.CaptureOut[1:]
	}
	return out, shiftError(&f.CaptureError)
}

func (f *Fake) Pipe(_ context.Context, producer Command, consumer Command) error {
	f.Records = append(f.Records, Recorded{Kind: "pipe-producer", Cmd: producer})
	f.Records = append(f.Records, Recorded{Kind: "pipe-consumer", Cmd: consumer})
	return shiftError(&f.PipeErrors)
}

func (f *Fake) Commands() [][]string {
	cmds := make([][]string, 0, len(f.Records))
	for _, record := range f.Records {
		cmds = append(cmds, append([]string(nil), record.Cmd.Argv...))
	}
	return cmds
}

func (f *Fake) Output() string {
	var buf bytes.Buffer
	for _, record := range f.Records {
		buf.WriteString(record.Kind)
		buf.WriteByte(' ')
		buf.WriteString(ShellCommand(record.Cmd.Argv))
		buf.WriteByte('\n')
	}
	return buf.String()
}

func shiftError(errs *[]error) error {
	if len(*errs) == 0 {
		return nil
	}
	err := (*errs)[0]
	*errs = (*errs)[1:]
	return err
}
