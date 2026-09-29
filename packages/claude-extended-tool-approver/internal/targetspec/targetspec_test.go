package targetspec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/specfmt"
)

func targetSpec(name string, kind TargetKind, class TargetClass) specfmt.Spec {
	return specfmt.Spec{
		Version: specfmt.FormatVersion,
		Kind:    specfmt.KindTarget,
		Name:    name,
		Target: &specfmt.TargetSpecV1{
			TargetKind: kind,
			Class:      class,
			Citation:   specfmt.Citation{Source: "test fixture"},
		},
	}
}

func writeSpecFile(t *testing.T, dir, filename string, spec specfmt.Spec) {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal fixture spec: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

// TestRepository_RoundTripAllThreeLayers loads a target-spec entry from each
// of the three layers (a distinct target in each, so precedence never has to
// arbitrate) and confirms Lookup.Class answers correctly for all three —
// this packet's Validation item 1 ("round-trip load of embedded + user +
// repo layers").
func TestRepository_RoundTripAllThreeLayers(t *testing.T) {
	embedded := fstest.MapFS{}
	embeddedData, err := json.Marshal(targetSpec("kagents", TargetKindKubeContext, TargetClassTrustedDev))
	if err != nil {
		t.Fatalf("marshal embedded fixture: %v", err)
	}
	embedded["kagents.json"] = &fstest.MapFile{Data: embeddedData}

	userDir := t.TempDir()
	writeSpecFile(t, userDir, "prod-vault.json", targetSpec("vault.prod.internal", TargetKindVaultAddress, TargetClassProduction))

	repoDir := t.TempDir()
	writeSpecFile(t, repoDir, "synfra-ssh.json", targetSpec("synfra.twistcone.us", TargetKindSSHHost, TargetClassTrustedDev))

	repo := NewRepository(embedded, userDir, repoDir)
	lookup, conflicts, invalid, err := repo.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("Conflicts = %#v, want none", conflicts)
	}
	if len(invalid) != 0 {
		t.Fatalf("Invalid = %#v, want none", invalid)
	}

	cases := []struct {
		name      string
		kind      TargetKind
		wantClass TargetClass
	}{
		{"kagents", TargetKindKubeContext, TargetClassTrustedDev},
		{"vault.prod.internal", TargetKindVaultAddress, TargetClassProduction},
		{"synfra.twistcone.us", TargetKindSSHHost, TargetClassTrustedDev},
	}
	for _, tc := range cases {
		class, found := lookup.Class(tc.name, tc.kind)
		if !found || class != tc.wantClass {
			t.Errorf("Class(%q, %q) = %q, found=%v, want %q, found=true", tc.name, tc.kind, class, found, tc.wantClass)
		}
	}

	if _, found := lookup.Class("unlisted", TargetKindKubeContext); found {
		t.Error("Class(unlisted) reported found=true")
	}
}

// TestRepository_UserOverridesEmbedded confirms the SAME precedence
// (embedded < user < repo) Commands already exercises applies unchanged to
// Targets.
func TestRepository_UserOverridesEmbedded(t *testing.T) {
	embeddedData, err := json.Marshal(targetSpec("kprod", TargetKindKubeContext, TargetClassTrustedDev))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	embedded := fstest.MapFS{"kprod.json": &fstest.MapFile{Data: embeddedData}}

	userDir := t.TempDir()
	override := targetSpec("kprod", TargetKindKubeContext, TargetClassProduction)
	override.Overrides = true
	writeSpecFile(t, userDir, "kprod.json", override)

	repo := NewRepository(embedded, userDir, "")
	lookup, conflicts, _, err := repo.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("Conflicts = %#v, want none (Overrides declared)", conflicts)
	}
	class, found := lookup.Class("kprod", TargetKindKubeContext)
	if !found || class != TargetClassProduction {
		t.Fatalf("Class(kprod) = %q, found=%v, want production", class, found)
	}
}

// TestLookup_NilIsUnlisted confirms a nil *Lookup (no Repository ever
// loaded) behaves identically to an empty one: every target is unlisted.
func TestLookup_NilIsUnlisted(t *testing.T) {
	var lookup *Lookup
	if _, found := lookup.Class("anything", TargetKindKubeContext); found {
		t.Error("nil Lookup reported found=true")
	}
}

// TestRepository_P6RollbackFixture (P6 rollback safety, this packet's
// Validation item 3): a sample rules.json-style legacy fixture carrying the
// OLD kubeContexts/remoteLifecycle keys (evalcontract.Request's own shape)
// alongside a target-spec migration of the SAME data still parses cleanly —
// migrating a kube context into the new target-spec format does not require
// removing or disturbing the old keys.
func TestRepository_P6RollbackFixture(t *testing.T) {
	// The legacy fixture: an operator's pre-P8 kubeContexts/remoteLifecycle
	// configuration (evalcontract.Request.KubeContexts/RemoteLifecycle's own
	// shape — see that package's doc comment). This package does not import
	// evalcontract (it has no reason to parse the legacy shape itself; P6
	// only requires that BOTH shapes remain independently parseable), so the
	// legacy side is represented here as the same plain
	// map[string]struct{Allow []string} / map[string]string shape
	// evalcontract.KubeContextRule/Request.RemoteLifecycle already are.
	type legacyKubeContextRule struct {
		Allow []string `json:"allow"`
	}
	type legacyFixture struct {
		KubeContexts    map[string]legacyKubeContextRule `json:"kubeContexts"`
		RemoteLifecycle map[string]string                `json:"remoteLifecycle"`
	}
	legacyJSON := []byte(`{
		"kubeContexts": {"prod-k8s": {"allow": ["read"]}},
		"remoteLifecycle": {"dolt": "reject"}
	}`)
	var legacy legacyFixture
	if err := json.Unmarshal(legacyJSON, &legacy); err != nil {
		t.Fatalf("legacy kubeContexts/remoteLifecycle fixture failed to parse: %v", err)
	}
	if legacy.KubeContexts["prod-k8s"].Allow[0] != "read" {
		t.Fatalf("legacy kubeContexts round-trip lost data: %#v", legacy.KubeContexts)
	}
	if legacy.RemoteLifecycle["dolt"] != "reject" {
		t.Fatalf("legacy remoteLifecycle round-trip lost data: %#v", legacy.RemoteLifecycle)
	}

	// The SAME target ("prod-k8s") migrated into the new target-spec format
	// (P8's own migration note: "Existing rules.json
	// kubeContexts/remoteLifecycle entries migrate into the target spec; the
	// old keys remain for rollback").
	migrated := targetSpec("prod-k8s", TargetKindKubeContext, TargetClassProduction)
	data, err := json.Marshal(migrated)
	if err != nil {
		t.Fatalf("marshal migrated target spec: %v", err)
	}
	var roundTripped specfmt.Spec
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("migrated target spec failed to parse: %v", err)
	}
	if err := specfmt.Validate(roundTripped); err != nil {
		t.Fatalf("migrated target spec failed Validate: %v", err)
	}

	repoDir := t.TempDir()
	writeSpecFile(t, repoDir, "prod-k8s.json", migrated)
	repo := NewRepository(nil, "", repoDir)
	lookup, _, invalid, err := repo.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(invalid) != 0 {
		t.Fatalf("Invalid = %#v, want none", invalid)
	}
	class, found := lookup.Class("prod-k8s", TargetKindKubeContext)
	if !found || class != TargetClassProduction {
		t.Fatalf("Class(prod-k8s) = %q, found=%v, want production", class, found)
	}
}
