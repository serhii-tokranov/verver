// Package cli implements Verver's command-line interface.
package cli

import (
	"fmt"
	"io"
)

const usage = `Verver — automatic versioning for Git pipelines.

Usage:
  verver --help
  verver --version

Options:
  -h, --help     Show this help
  --version      Show the CLI version
`

// Run executes the CLI. It returns 0 on success, 1 on an output failure,
// and 2 for invalid arguments. The caller owns the supplied streams.
func Run(args []string, stdout, stderr io.Writer, version string) int {
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
