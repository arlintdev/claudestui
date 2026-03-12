//go:build !windows

package tmux

import (
	"os"
	"syscall"
)

// execvp replaces the current process with the given command.
func execvp(path string, args []string) error {
	return syscall.Exec(path, args, os.Environ())
}
