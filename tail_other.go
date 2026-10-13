//go:build !unix

package main

import "os"

// fileID is unavailable on this platform; rotation falls back to the
// truncation rule (size below the consumed offset).
func fileID(fi os.FileInfo) (dev, ino uint64, ok bool) {
	return 0, 0, false
}
