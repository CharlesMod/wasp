// Package atomicfile writes a file whole or not at all: the bytes go to a temp
// file in the destination's directory, are synced, and the temp file is renamed
// over the destination, so a reader finds the old bytes or the new ones, never
// part of either.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write puts data at path with perm, whole or not at all. On failure the
// destination is untouched and no temp file is left behind.
func Write(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}
