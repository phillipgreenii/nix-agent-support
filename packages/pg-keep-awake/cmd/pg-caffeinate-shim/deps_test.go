package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestShimDependsOnNeitherConfigNorTelemetry pins that the shim reads no
// configuration and initializes no OTel: neither the config package nor any
// telemetry package may appear among its dependencies.
func TestShimDependsOnNeitherConfigNorTelemetry(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		goTool = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	out, err := exec.Command(goTool, "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) == 0 {
		t.Fatal("go list -deps returned nothing")
	}
	for _, d := range deps {
		switch {
		case strings.HasSuffix(d, "/internal/config"),
			strings.Contains(d, "telemetry"),
			strings.Contains(d, "opentelemetry"),
			strings.Contains(d, "BurntSushi/toml"):
			t.Errorf("the shim depends on %s", d)
		}
	}
}
