package notify_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/notify"
)

type call struct {
	name string
	args []string
}

func recorder(err error) (notify.Runner, *[]call) {
	var calls []call
	return func(_ context.Context, name string, args ...string) error {
		calls = append(calls, call{name, args})
		return err
	}, &calls
}

func TestPlayResolvesTheNameToASoundFileAndRunsAfplay(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Glass.aiff"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	run, calls := recorder(nil)
	s := notify.System{Run: run, SoundDirs: []string{filepath.Join(dir, "missing"), dir}}
	if err := s.Play(context.Background(), "Glass"); err != nil {
		t.Fatal(err)
	}
	want := []call{{"afplay", []string{filepath.Join(dir, "Glass.aiff")}}}
	if !reflect.DeepEqual(*calls, want) {
		t.Errorf("calls = %v, want %v", *calls, want)
	}
}

func TestPlayRefusesANameThatIsNotAPlainName(t *testing.T) {
	run, calls := recorder(nil)
	s := notify.System{Run: run, SoundDirs: []string{t.TempDir()}}
	for _, name := range []string{"../../etc/passwd", "a/b", "", "Glass.aiff", strings.Repeat("x", 65), "a;b"} {
		if err := s.Play(context.Background(), name); err == nil {
			t.Errorf("Play(%q) succeeded", name)
		}
	}
	if err := s.Play(context.Background(), "Nonesuch"); err == nil || !strings.Contains(err.Error(), "no sound named") {
		t.Errorf("an unknown sound: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("a program ran for a refused name: %v", *calls)
	}
}

func TestNotifyPassesTheTextAsArgumentsNeverInTheScript(t *testing.T) {
	run, calls := recorder(nil)
	title, body := `Deep "work" \ cycle`, `5 min over"; do shell script "x"`
	if err := (notify.System{Run: run}).Notify(context.Background(), title, body); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.name != "osascript" || len(c.args) != 4 || c.args[2] != body || c.args[3] != title {
		t.Fatalf("call = %+v", c)
	}
	if strings.Contains(c.args[1], "work") || strings.Contains(c.args[1], "shell script") {
		t.Errorf("the script holds the text: %q", c.args[1])
	}
}

func TestAFailureIsReturnedNotSwallowed(t *testing.T) {
	boom := errors.New("no audio device")
	run, _ := recorder(boom)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "Glass.aiff"), nil, 0o600)
	s := notify.System{Run: run, SoundDirs: []string{dir}}
	if err := s.Play(context.Background(), "Glass"); !errors.Is(err, boom) {
		t.Errorf("Play = %v", err)
	}
	if err := s.Notify(context.Background(), "t", "b"); !errors.Is(err, boom) {
		t.Errorf("Notify = %v", err)
	}
}

func TestFakeRecordsAndFails(t *testing.T) {
	f := &notify.Fake{PlayErr: errors.New("x")}
	if err := f.Play(context.Background(), "Glass"); err == nil {
		t.Error("the configured error was not returned")
	}
	_ = f.Notify(context.Background(), "t", "b")
	if !reflect.DeepEqual(f.Played(), []string{"Glass"}) || len(f.Notified()) != 1 {
		t.Errorf("recorded %v, %v", f.Played(), f.Notified())
	}
}
