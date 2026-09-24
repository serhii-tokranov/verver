// Package cli implements Verver's command-line interface.
package cli

import (
	"context"
	"fmt"
	"io"
)

const usage = `Verver — automatic versioning for Git pipelines.

Usage:
  verver --help
  verver --version
  verver next --ref refs/heads/<branch> [options]
  verver release --ref refs/heads/<branch> --serialized [options]
  verver check-pr --github owner/repo --pr <number> [options]

Run a command with --help for its options. Release requires a shared CI lock.

Options:
  -h, --help     Show this help
  --version      Show the CLI version
`

// Run executes the CLI. It returns 0 on success, 1 on an operational failure,
// and 2 for invalid arguments. The caller owns the supplied streams.
func Run(args []string, stdout, stderr io.Writer, version string) int {
	return RunContext(context.Background(), args, stdout, stderr, version)
}

// RunContext supports cancellation of repository and network operations.
func RunContext(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	if len(args) > 0 && (args[0] == "next" || args[0] == "release" || args[0] == "check-pr") {
		return runCommand(ctx, args[0], args[1:], stdout, stderr)
	}
	var output string
	switch {
	case len(args) == 0:
		output = usage
	case len(args) == 1 && (args[0] == "--help" || args[0] == "-h"):
		output = usage
	case len(args) == 1 && args[0] == "--version":
		output = "verver " + version + "\n"
	default:
		fmt.Fprintln(stderr, "verver: invalid arguments; run 'verver --help' for usage")
		return 2
	}
	if _, err := io.WriteString(stdout, output); err != nil {
		fmt.Fprintln(stderr, "verver: could not write output")
		return 1
	}
	return 0
}
