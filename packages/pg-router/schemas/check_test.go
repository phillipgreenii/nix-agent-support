package schemas

import "testing"

// Defensive: the cross-field rules reject a non-object value directly.
func TestCrossFieldRules_NonObject(t *testing.T) {
	if storeRequestRule(42) == nil {
		t.Fatal("storeRequestRule accepted a non-object")
	}
}

// Check applies the cross-field rule after the structural schema: value is
// required if and only if op is put.
func TestCheckEnforcesStoreCrossFieldRule(t *testing.T) {
	put := map[string]any{"schemaVersion": "1", "id": "s", "op": "put", "key": "k"}
	if Check("store.request", put) == nil {
		t.Fatal("op=put without a value was accepted")
	}
	get := map[string]any{"schemaVersion": "1", "id": "s", "op": "get", "key": "k", "value": "v"}
	if Check("store.request", get) == nil {
		t.Fatal("op=get with a value was accepted")
	}
	if CheckBytes("store.request", []byte(`{"schemaVersion":"1","id":"s","op":"get","key":"k","value":"v"}`)) == nil {
		t.Fatal("CheckBytes accepted a cross-field violation")
	}
}
