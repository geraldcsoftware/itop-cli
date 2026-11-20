// Package cli is the command dispatcher for the itop binary.
package cli

import (
	"fmt"
	"io"
)

// BuildInfo identifies the running build; the linker sets it through main.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

var build = BuildInfo{Version: "dev", Commit: "none", Date: "unknown"}

// SetBuildInfo records the build identity reported by `itop version`.
func SetBuildInfo(b BuildInfo) { build = b }

const usage = `itop reads an iTop ITSM instance over its REST API.

Usage:
  itop <command> [flags]

Commands:
  version    print the build version
`

// Run executes the command line and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 1
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "itop %s (%s, %s)\n", build.Version, build.Commit, build.Date)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 1
	}
}
