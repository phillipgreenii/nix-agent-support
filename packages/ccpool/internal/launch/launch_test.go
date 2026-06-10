package launch

import (
	"reflect"
	"testing"
)

func TestBuildNew(t *testing.T) {
	got := BuildNew(Spec{
		ClaudeBin: "claude", UUID: "u1", Name: "alpha", PluginDir: "/nix/plugin", Model: "opus",
	})
	want := []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/nix/plugin", "--model", "opus"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildNew = %v\nwant %v", got, want)
	}
}

func TestBuildNew_omitsModelWhenEmpty(t *testing.T) {
	got := BuildNew(Spec{ClaudeBin: "claude", UUID: "u1", Name: "alpha", PluginDir: "/p"})
	want := []string{"claude", "--session-id", "u1", "--name", "alpha", "--plugin-dir", "/p"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildNew = %v\nwant %v", got, want)
	}
}

func TestBuildResume_resumesByName_alwaysHasPluginDir(t *testing.T) {
	got := BuildResume(Spec{ClaudeBin: "claude", Name: "alpha", PluginDir: "/p"})
	want := []string{"claude", "--resume", "alpha", "--plugin-dir", "/p"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BuildResume = %v\nwant %v", got, want)
	}
}
