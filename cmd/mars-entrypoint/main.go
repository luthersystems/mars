package main

import (
	"fmt"
	"os"

	"github.com/luthersystems/mars/internal/entrypoint"
)

func main() {
	if err := entrypoint.Run("/opt/mars/mars", os.Args[1:], os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
