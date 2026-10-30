// Package notify shows the overtime banner, with its sound, through the
// pg-osx-notifier daemon. Notifier is the seam (a Strategy) so the daemon's
// tests need no desktop: a fake records what it was asked to post.
//
// The system implementation posts one banner over the daemon's socket, the
// sound name in the request. When the daemon is unreachable or refuses, Notify
// returns an error, which the alerter logs and counts and which never touches
// cycle state. History: the sound used to play through a separate player even
// when the banner failed; now one post carries both, so a denied notification
// permission silences both.
package notify

import (
	"context"
	"regexp"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/notifyclient"
)

// Notifier shows one banner with an optional sound (empty means none given).
type Notifier interface {
	Notify(ctx context.Context, title, body, sound string) error
}

// soundName is the form of a sound name the daemon accepts: letters, digits,
// space, underscore and hyphen. It keeps a name from naming a path.
var soundName = regexp.MustCompile(`^[A-Za-z0-9 _-]{1,64}$`)

// ValidSoundName reports whether name is a sound name the daemon accepts.
func ValidSoundName(name string) bool { return soundName.MatchString(name) }

// System posts to the notifier daemon. The zero value follows the socket
// discovery rule and the 7 second deadline.
type System struct {
	Client notifyclient.Client
}

// Notify posts one banner.
func (s System) Notify(ctx context.Context, title, body, sound string) error {
	return s.Client.Post(ctx, title, body, sound)
}

// Fake records every request and fails on demand. It is safe for concurrent
// use.
type Fake struct {
	mu            sync.Mutex
	Notifications []Notification
	NotifyErr     error
	// Panic, when set, makes Notify panic with it.
	Panic any
	// Block, when set, holds every Notify until it is closed.
	Block chan struct{}
}

// Notification is one recorded banner.
type Notification struct{ Title, Body, Sound, Source string }

// Notify records the banner.
func (f *Fake) Notify(_ context.Context, title, body, sound string) error {
	if f.Block != nil {
		<-f.Block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Panic != nil {
		panic(f.Panic)
	}
	f.Notifications = append(f.Notifications, Notification{title, body, sound, notifyclient.Source})
	return f.NotifyErr
}

// Notified returns a copy of the banners sent so far.
func (f *Fake) Notified() []Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Notification(nil), f.Notifications...)
}

// Sounds returns the sound of each recorded banner, in order.
func (f *Fake) Sounds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Notifications))
	for i, n := range f.Notifications {
		out[i] = n.Sound
	}
	return out
}
