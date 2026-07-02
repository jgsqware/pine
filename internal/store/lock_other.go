//go:build !unix

package store

import "os"

// lockDir is a no-op on platforms without flock (e.g. Windows); the store falls
// back to its in-process mutex only.
func lockDir(dir string) (*os.File, error) { return nil, nil }
