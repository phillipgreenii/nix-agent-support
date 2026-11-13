//go:build !darwin

package host

// SystemNative is nil off darwin: every key goes through the command seam.
var SystemNative Native
