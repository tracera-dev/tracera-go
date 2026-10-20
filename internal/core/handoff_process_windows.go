//go:build windows

package core

import (
	"errors"
	"os"
	"syscall"
)

// processAlive reports whether pid exists.
// Process.Signal(0) returns EWINDOWS for every signal other than Kill, so it
// cannot probe liveness. FindProcess opens the process; access denied means it
// exists (same idea as POSIX EPERM).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return windowsAccessDenied(err)
	}
	_ = proc.Release()
	return true
}

func windowsAccessDenied(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.ERROR_ACCESS_DENIED
}
