package main

import (
	"os"

	internal "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-agentsession-pa-monitor/internal"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/agentsession"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

var Version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	backend := internal.New(internal.NewCLIRunner())
	return scriptout.ServeLoop(newDispatchTable(backend))
}

func newDispatchTable(backend *internal.Backend) scriptout.DispatchTable {
	table := agentsession.NewDispatchTable(backend)
	return scriptout.AddCapabilities(table, schema.AgentSessionSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"agentsession": schema.AgentSessionSchemaVersion},
		Version:         Version,
	})
}
