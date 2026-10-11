//go:build !windows

package updater

import "os"

func applySwap(current, old, staged string) error {
	if err := os.Rename(current, old); err != nil {
		return err
	}
	if err := os.Rename(staged, current); err != nil {
		_ = os.Rename(old, current)
		return err
	}
	return nil
}

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
