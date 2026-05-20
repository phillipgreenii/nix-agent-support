package main

import "testing"

func TestVersionStringNotEmpty(t *testing.T) {
	if version == "" {
		t.Fatal("version must not be empty")
	}
}
