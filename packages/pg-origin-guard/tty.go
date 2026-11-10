package originguard

import (
	"os"

	"golang.org/x/sys/unix"
)

// FileIsTerminal reports whether f is a terminal.
func FileIsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil
}

// StderrIsTerminal reports whether the process's stderr is a terminal.
func StderrIsTerminal() bool { return FileIsTerminal(os.Stderr) }

// CallerHasTerminal is rule W2: stderr is a terminal OR /dev/tty opens.
func CallerHasTerminal() bool {
	if StderrIsTerminal() {
		return true
	}
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
