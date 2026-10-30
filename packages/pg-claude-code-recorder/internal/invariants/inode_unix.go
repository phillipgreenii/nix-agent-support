//go:build unix

package invariants

import (
	"fmt"
	"io/fs"
	"syscall"
)

func inodeOf(fi fs.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprint(st.Ino)
	}
	return "?"
}
