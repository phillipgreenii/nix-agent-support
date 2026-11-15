package tokenfile

import (
	"os"
	"syscall"
	"time"
)

func atime(fi os.FileInfo) time.Time {
	st := fi.Sys().(*syscall.Stat_t)
	return time.Unix(st.Atim.Sec, st.Atim.Nsec)
}
