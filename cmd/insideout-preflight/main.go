// Command insideout-preflight runs fail-fast bootstrap-permission preflights
// for InsideOut cloud deploys (luthersystems/reliable#2243).
//
// It is the Go port of the sandbox-infrastructure-template shell preflights
// (tf/gcp-preflight.sh, tf/aws-preflight.sh). The template keeps the hook, the
// SKIP_*_BOOTSTRAP_PREFLIGHT escape hatches, and the permission/action lists;
// this binary takes those lists as INPUT and supplies the mechanics with typed
// SDK error classification.
//
// See package github.com/luthersystems/mars/internal/preflight for the full CLI
// contract (flags, exit codes, and output markers).
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/luthersystems/mars/internal/preflight"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	os.Exit(preflight.Main(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
