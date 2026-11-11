// Command pg-router-sensors is the ONE generic binary of the router's sensors:
// each subcommand (host-sensor, vpn-sensor, git-auth-sensor, beads-sensor,
// claude-sensor) is a pull query that prints one JSONL level answer per
// resource. Hosts, remotes, tracker names and certificate margins are
// configuration, never code.
package main

import (
	"fmt"
	"os"

	"github.com/phillipgreenii/pg-router-sensors/internal/beads"
	"github.com/phillipgreenii/pg-router-sensors/internal/gitauth"
	"github.com/phillipgreenii/pg-router-sensors/internal/host"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
	"github.com/phillipgreenii/pg-router-sensors/internal/vpn"
)

// version is stamped by the build (versionPath = main.version).
var version = "dev"

// registry holds the subcommands whose packets have landed; an unregistered
// subcommand prints usage and exits 2.
func registry() sensorcli.Registry {
	return sensorcli.Registry{"host-sensor": host.New(), "vpn-sensor": vpn.New(), "git-auth-sensor": gitauth.New(), "beads-sensor": beads.New()}
}

func main() {
	args := os.Args[1:]
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Println(version)
		return
	}
	os.Exit(sensorcli.Run(args, registry(), sensorcli.IO{Stdout: os.Stdout, Stderr: os.Stderr}))
}
