//go:build unix

package main

import (
	"os"
	"syscall"
)

// fileID returns the device and inode of a file so rotation (a different
// file appearing at the same path) can be told apart from truncation.
func fileID(fi os.FileInfo) (dev, ino uint64, ok bool) {
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}
