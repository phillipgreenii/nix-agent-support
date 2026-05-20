package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeState_AtomicRoundTrip(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "runtime.json")
	s := RuntimeState{CaffeinateOn: true}
	if err := WriteRuntimeState(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRuntimeState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CaffeinateOn {
		t.Error("CaffeinateOn lost")
	}
}

func TestRuntimeState_MissingFileReturnsZero(t *testing.T) {
	got, err := ReadRuntimeState("/no/such/file")
	if err != nil {
		t.Fatalf("expected (zero, nil), got err %v", err)
	}
	if got.CaffeinateOn {
		t.Error("zero state should have CaffeinateOn=false")
	}
}

func TestRuntimeState_BadJSONReturnsZeroAndError(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "runtime.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRuntimeState(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if got.CaffeinateOn {
		t.Error("zero state expected on parse failure")
	}
}
