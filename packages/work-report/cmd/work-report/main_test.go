package main

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

func TestRootDeclaresGlobalFlags(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"config", "output", "store"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("root command lacks persistent flag --%s", name)
		}
	}
	if root.Use != "work-report" {
		t.Errorf("root Use = %q, want work-report", root.Use)
	}
}

func TestHelpListsGlobalFlags(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{"--config", "--output", "--store"} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Errorf("help output lacks %s:\n%s", want, out.String())
		}
	}
}

func TestRegisterCommandAndGlobalFlags(t *testing.T) {
	saved := registry
	t.Cleanup(func() { registry = saved })

	type seen struct{ config, output, store string }
	var got seen
	registerCommand(func() *cobra.Command {
		return &cobra.Command{
			Use: "probe",
			RunE: func(cmd *cobra.Command, _ []string) error {
				got.config, got.output, got.store = globalFlags(cmd)
				return nil
			},
		}
	})

	root := newRootCmd()
	root.SetArgs([]string{"probe", "--config", "/c.yaml", "--output", "json", "--store", "/s.db"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := (seen{"/c.yaml", "json", "/s.db"}); got != want {
		t.Errorf("globalFlags = %+v, want %+v", got, want)
	}

	got = seen{"x", "x", "x"}
	root = newRootCmd()
	root.SetArgs([]string{"probe"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := (seen{}); got != want {
		t.Errorf("unset globalFlags = %+v, want all empty", got)
	}
}
