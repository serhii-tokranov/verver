package main

import (
	"os"

	"verver/internal/cli"
)

// Set by release builds with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, version))
}
