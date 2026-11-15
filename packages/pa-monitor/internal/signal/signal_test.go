package signal_test

import (
	"errors"
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/signal"
)

func TestRequiredBinaries(t *testing.T) {
	if got := (&signal.TmuxSignaler{}).RequiredBinaries(); len(got) != 1 || got[0] != "tmux" {
		t.Errorf("tmux RequiredBinaries = %v", got)
	}
	if got := (&signal.CmuxSignaler{}).RequiredBinaries(); len(got) != 1 || got[0] != "cmux" {
		t.Errorf("cmux RequiredBinaries = %v", got)
	}
}

func TestMissingBinariesReportsUnresolvable(t *testing.T) {
	look := func(bin string) (string, error) {
		if bin == "tmux" {
			return "", errors.New("not found")
		}
		return "/bin/" + bin, nil
	}
	got := signal.MissingBinaries(signal.DefaultSignalers(), look)
	if len(got) != 1 || got[0].Signaler != "tmux" || got[0].Binary != "tmux" {
		t.Errorf("MissingBinaries = %+v, want one tmux entry", got)
	}
}

func TestMissingBinariesEmptyWhenAllResolvable(t *testing.T) {
	look := func(bin string) (string, error) { return "/bin/" + bin, nil }
	if got := signal.MissingBinaries(signal.DefaultSignalers(), look); len(got) != 0 {
		t.Errorf("MissingBinaries = %+v, want none", got)
	}
}
