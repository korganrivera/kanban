//go:build !windows

package ops

import (
	"fmt"
	"os"
	"path/filepath"
)

func syncPath(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func updateLatest(root, target string) error {
	temporary := filepath.Join(root, fmt.Sprintf(".latest-%d", os.Getpid()))
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(root, "latest")); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
