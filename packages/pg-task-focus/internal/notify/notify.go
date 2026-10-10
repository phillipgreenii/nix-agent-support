// Package notify plays the overtime sound and sends the notification that go
// with it. Both are behind small interfaces (the Strategy pattern the design
// names for SoundPlayer) so the daemon's tests need no audio and no desktop:
// a fake records what it was asked to do.
//
// The system implementation is macOS: a sound name is resolved to an .aiff
// file in the sound folders and played with afplay, and a notification is shown
// with osascript, the text passed as arguments and never spliced into the
// script. On a host without those tools both return an error, which the
// daemon logs and counts and which never touches cycle state.
package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// SoundPlayer plays one short sound by name.
type SoundPlayer interface {
	Play(ctx context.Context, name string) error
}

// Notifier shows one desktop notification.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
}

// soundName is the form of a sound name this package resolves: letters,
// digits, space, underscore and hyphen. It keeps a name from naming a path.
var soundName = regexp.MustCompile(`^[A-Za-z0-9 _-]{1,64}$`)

// ValidSoundName reports whether name is a sound name the system player
// would try to resolve.
func ValidSoundName(name string) bool { return soundName.MatchString(name) }

// Runner runs a program to completion.
type Runner func(ctx context.Context, name string, args ...string) error

func execRunner(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		// The output is dropped on purpose: it could echo a sound or a title.
		_ = out
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// System plays sounds with afplay and notifies with osascript.
type System struct {
	// Run runs a program; nil means os/exec.
	Run Runner
	// SoundDirs are searched in order for <name>.aiff; empty means the
	// system, the machine and the user's sound folders.
	SoundDirs []string
	// Timeout bounds one playback or notification; zero means 10 seconds.
	Timeout time.Duration
}

func (s System) run() Runner {
	if s.Run != nil {
		return s.Run
	}
	return execRunner
}

func (s System) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 10 * time.Second
}

func (s System) dirs() []string {
	if len(s.SoundDirs) > 0 {
		return s.SoundDirs
	}
	dirs := []string{"/System/Library/Sounds", "/Library/Sounds"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Library", "Sounds"))
	}
	return dirs
}

// Play plays the named sound.
func (s System) Play(ctx context.Context, name string) error {
	if !ValidSoundName(name) {
		return fmt.Errorf("the sound name %q is not a plain name", name)
	}
	var file string
	for _, d := range s.dirs() {
		p := filepath.Join(d, name+".aiff")
		if _, err := os.Stat(p); err == nil {
			file = p
			break
		}
	}
	if file == "" {
		return fmt.Errorf("no sound named %q in the sound folders", name)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	return s.run()(ctx, "afplay", file)
}

// notifyScript takes the body and the title as arguments.
const notifyScript = "on run argv\ndisplay notification (item 1 of argv) with title (item 2 of argv)\nend run"

// Notify shows a notification.
func (s System) Notify(ctx context.Context, title, body string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	return s.run()(ctx, "osascript", "-e", notifyScript, body, title)
}

// Fake records every request and fails on demand. It is safe for concurrent
// use.
type Fake struct {
	mu            sync.Mutex
	Sounds        []string
	Notifications []Notification
	PlayErr       error
	NotifyErr     error
}

// Notification is one recorded notification.
type Notification struct{ Title, Body string }

// Play records the sound.
func (f *Fake) Play(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sounds = append(f.Sounds, name)
	return f.PlayErr
}

// Notify records the notification.
func (f *Fake) Notify(_ context.Context, title, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Notifications = append(f.Notifications, Notification{title, body})
	return f.NotifyErr
}

// Played returns a copy of the sounds played so far.
func (f *Fake) Played() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.Sounds...)
}

// Notified returns a copy of the notifications sent so far.
func (f *Fake) Notified() []Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Notification(nil), f.Notifications...)
}

// Unavailable is what a player or notifier reports on a host with no way to
// do it.
var Unavailable = errors.New("no sound or notification facility on this host")
