package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderReconcile_empty(t *testing.T) {
	var b bytes.Buffer
	renderReconcile(&b, "phillipg", nil)
	out := b.String()
	if !strings.Contains(out, "no stranded") {
		t.Errorf("empty report should say no stranded cycles; got %q", out)
	}
	if !strings.Contains(out, "phillipg") {
		t.Errorf("report should name self; got %q", out)
	}
}

func TestRenderReconcile_listsCyclesAndBackfillCommand(t *testing.T) {
	var b bytes.Buffer
	renderReconcile(&b, "phillipg", []string{"zr-100", "zr-101"})
	out := b.String()
	for _, want := range []string{"2 stranded", "zr-100", "zr-101", "add-label mine"} {
		if !strings.Contains(out, want) {
			t.Errorf("report should contain %q; got %q", want, out)
		}
	}
}
