//go:build !darwin

package browser

// openWindow reports ErrUnsupported: this repo's modules build for NixOS as
// well as nix-darwin, and there is no Linux equivalent of Chrome's
// forward-argv-to-running-instance behaviour that also guarantees a single NEW
// window with one tab per URL. Failing loudly beats opening nothing quietly.
func openWindow(_ []string) error { return ErrUnsupported }
