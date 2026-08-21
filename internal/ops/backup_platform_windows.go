//go:build windows

package ops

import (
	"fmt"
	"os"
	"path/filepath"
)

func syncPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func updateLatest(root, target string) error {
	temporary := filepath.Join(root, fmt.Sprintf(".latest-%d.txt", os.Getpid()))
	final := filepath.Join(root, "latest.txt")
	_ = os.Remove(temporary)
	if err := os.WriteFile(temporary, []byte(target+"\n"), 0o600); err != nil {
		return err
	}
	_ = os.Remove(final)
	if err := os.Rename(temporary, final); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
