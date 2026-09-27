// Package atomicfile places a file at its final path only when its content is
// complete and accepted.
//
// A download that fails half way, or whose checksum does not match, must not
// leave a file at the path another program reads. The content is therefore
// written to a temporary file in the same directory and renamed into place
// only after it is verified. A rename within one directory is atomic, so a
// reader sees the old file or the new one, never a part of either.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Mode is the permission of a placed file. os.CreateTemp creates 0600, which
// would hide the file from a consumer that runs as another user, so the
// temporary file is widened to the mode os.Create gives under the usual umask.
const Mode = 0o644

// Write creates dst with the content fill writes to f. fill writes the
// content and may verify it: when fill returns an error, nothing is placed.
//
// The temporary file is named ".<base>.part-*" in the directory of dst. It is
// synced, closed and renamed to dst only when fill succeeds, and it is removed
// on every error. A dst that existed before keeps its old content when Write
// fails.
func Write(dst string, fill func(f *os.File) error) (err error) {
	f, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".part-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()

	if err := f.Chmod(Mode); err != nil {
		return err
	}
	if err := fill(f); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	// A failed close can be the first report of a failed write, on a full
	// disk or a network file system, so it is an error like any other.
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	return nil
}
