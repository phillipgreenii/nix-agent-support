package notifyclient

import "testing"

func TestSocketDiscovery(t *testing.T) {
	for name, tc := range map[string]struct {
		env     map[string]string
		want    string
		wantErr bool
	}{
		"explicit":  {map[string]string{"PG_OSX_NOTIFIER_SOCKET": "/x/s", "XDG_STATE_HOME": "/st", "HOME": "/h"}, "/x/s", false},
		"xdg":       {map[string]string{"XDG_STATE_HOME": "/st", "HOME": "/h"}, "/st/pg-osx-notifier/pg-osx-notifier.sock", false},
		"home":      {map[string]string{"HOME": "/h"}, "/h/.local/state/pg-osx-notifier/pg-osx-notifier.sock", false},
		"no inputs": {map[string]string{}, "", true},
	} {
		got, err := Client{Getenv: func(k string) string { return tc.env[k] }}.socket()
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: got %q, %v; want %q (err %v)", name, got, err, tc.want, tc.wantErr)
		}
	}
}
