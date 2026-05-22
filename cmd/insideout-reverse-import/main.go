package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/luthersystems/mars/internal/reverseimportjob"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	os.Exit(reverseimportjob.Main(ctx, os.Args[1:], os.Stdout, os.Stderr, nil))
}
