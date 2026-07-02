//go:build unix

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockDir takes an exclusive, non-blocking advisory lock on the data directory
// so two Pine processes can't write the same JSON store and corrupt it. The
// returned handle must stay open for the process lifetime (the OS drops the
// lock on exit); Store holds it.
func lockDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, ".pine.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("data dir %q is already in use by another Pine process — "+
			"stop it, point --data elsewhere, or use `pine attach` to drive the running daemon", dir)
	}
	return f, nil
}
