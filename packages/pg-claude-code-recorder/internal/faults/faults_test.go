package faults

import "testing"

func TestAtIsNoOpWhenUnarmed(t *testing.T) {
	At("copy.after-fsync")
}

func TestArmPanicAndRecover(t *testing.T) {
	disarm := ArmPanic("p1")
	if !Recover(func() { At("p1") }) {
		t.Fatal("armed point did not crash")
	}
	disarm()
	if Recover(func() { At("p1") }) {
		t.Fatal("disarmed point crashed")
	}
}
