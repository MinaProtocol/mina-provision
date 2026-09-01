// mina-provision fetches and places the published artifacts a Mina daemon,
// archive node, block producer or Rosetta stack needs before it can start:
// archive database dumps, precomputed blocks and runtime configuration files.
//
// It only fetches, verifies and places files. Writing blocks into an archive
// database is mina-archive's own work.
//
// See README.md for usage and design notes.
package main

import (
	"os"

	"github.com/MinaProtocol/mina-provision/cmd"
)

// version is set at build time with -ldflags "-X main.version=...". It stays
// "dev" for a plain `go build`.
var version = "dev"

func main() {
	cmd.SetVersion(version)

	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
