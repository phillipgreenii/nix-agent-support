package provision

import "testing"

func TestWorktreeProvisioner(t *testing.T) {
	runProvisionerSuite(t, func(t *testing.T, cfg WorktreeConfig) Provisioner {
		w, err := NewWorktree(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return w
	})
}
