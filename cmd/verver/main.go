package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/serhii-tokranov/verver/internal/cli"
)

// Set by release builds with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.RunContext(ctx, os.Args[1:], os.Stdout, os.Stderr, version))
}
