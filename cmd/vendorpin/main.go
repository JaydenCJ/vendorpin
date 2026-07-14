// Command vendorpin vendors a subdirectory of another git repository at a
// pinned commit, records provenance in a lockfile, and detects drift.
package main

import (
	"os"

	"github.com/JaydenCJ/vendorpin/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
