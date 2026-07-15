package preflight

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// Exit codes — the whole contract. See the package doc.
const (
	exitOK    = 0 // passed OR fail-open
	exitFail  = 1 // definitive fail-closed
	exitUsage = 2 // usage error
)

// Escape-hatch env var names. The template hook honors these (it just does not
// invoke the binary when set); the binary references them in remediation text
// so the byte-for-byte failure blocks match the shell scripts.
const (
	skipGCP = "SKIP_GCP_BOOTSTRAP_PREFLIGHT"
	skipAWS = "SKIP_AWS_BOOTSTRAP_PREFLIGHT"
)

// Log prefixes — must match the shell scripts byte-for-byte (log tooling and
// the template tests grep on these, e.g. `^\[gcp-preflight\]   - `).
const (
	gcpPrefix = "[gcp-preflight] "
	awsPrefix = "[aws-preflight] "
)

// Main is the binary entry point. It dispatches on the first argument to the
// gcp or aws subcommand and returns the process exit code.
//
// A panic in any subcommand is recovered here and converted to fail-open (exit
// 0). The preflight is advisory, so a bug in our OWN mechanics must never brick
// a deploy: an uncaught panic would abort the process with a non-zero status
// (Go uses 2) that a hook could treat as fatal, whereas exit 0 is universally
// non-fatal. Recovering also avoids dumping a raw stack trace into deploy logs.
func Main(ctx context.Context, args []string, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fprintf(stderr, "[insideout-preflight] WARNING: internal error (%v) — preflight is advisory, continuing (deploy NOT blocked).\n", r)
			code = exitOK
		}
	}()

	if len(args) == 0 {
		printUsage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "gcp":
		return runGCP(ctx, args[1:], stdout, stderr)
	case "aws":
		return runAWS(ctx, args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printUsage(stdout)
		return exitOK
	default:
		fprintf(stderr, "insideout-preflight: unknown subcommand %q\n", args[0])
		printUsage(stderr)
		return exitUsage
	}
}

// fprintf / fprintln write to w, discarding the write error. A failed write to a
// terminal/pipe on usage or diagnostic output is not actionable, so these keep
// the call sites uncluttered while staying errcheck-clean.
func fprintf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
func fprintln(w io.Writer, a ...any)               { _, _ = fmt.Fprintln(w, a...) }

func printUsage(w io.Writer) {
	_, _ = fmt.Fprint(w, `insideout-preflight — InsideOut bootstrap-permission preflight (reliable#2243)

Usage:
  insideout-preflight gcp --project-id <PID> --credentials-file <SA-JSON> --permissions p1,p2,... [--timeout 30s]
  insideout-preflight aws --actions a1,a2,... [--role-arn <ARN> [--external-id <ID>]] [--region <r>] [--timeout 30s]

Exit codes:
  0  passed OR fail-open (advisory warning; deploy not blocked)
  1  definitive fail-closed (missing permissions/actions or bad credential)
  2  usage error (bad flags, empty list, unreadable credentials file)
`)
}

// stringList is a flag.Value that accepts comma-separated values and/or repeated
// flags, e.g. `--permissions a,b --permissions c` → [a b c]. Blank entries are
// dropped.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

// parseFlags parses a subcommand's flag set, translating flag errors into the
// usage exit code. ok=false means the caller should return the code as-is
// (either exitUsage on error or exitOK on -h/--help).
func parseFlags(fs *flag.FlagSet, args []string) (code int, ok bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	return exitOK, true
}

// logger writes prefixed lines to the info (stdout) and warn/error (stderr)
// streams, reproducing the shell scripts' log()/err() split. Info lines
// (progress, OK) go to out; warnings and failure blocks go to err.
type logger struct {
	prefix string
	out    io.Writer
	err    io.Writer
}

// outln writes an informational, prefixed line to stdout.
func (l logger) outln(s string) { fprintln(l.out, l.prefix+s) }

// errln writes a warning/error, prefixed line to stderr. An empty s reproduces
// the shell's `err ""` blank separator (prefix only).
func (l logger) errln(s string) { fprintln(l.err, l.prefix+s) }

// failOpen logs the advisory warning trio and returns exitOK. The preflight
// never blocks a deploy on its own flakiness (network, tooling, inability to
// verify). skipVar names the operator escape hatch for the closing line.
func (l logger) failOpen(reason, skipVar string) int {
	l.errln("WARNING: " + reason)
	l.errln("WARNING: preflight is advisory — continuing (deploy NOT blocked).")
	l.errln("WARNING: set " + skipVar + "=1 to silence this check.")
	return exitOK
}
