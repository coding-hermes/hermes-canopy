//go:build windows

package updater

import (
	"fmt"
	"syscall"
	"unsafe"
)

const moveFileReplaceExisting = 0x1

var (
	kernel32    = syscall.NewLazyDLL("kernel32.dll")
	moveFileExW = kernel32.NewProc("MoveFileExW")
)

func moveFile(source, destination string) error {
	src, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	dst, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := moveFileExW.Call(uintptr(unsafe.Pointer(src)), uintptr(unsafe.Pointer(dst)), moveFileReplaceExisting)
	if result == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return fmt.Errorf("MoveFileExW failed")
	}
	return nil
}

func applySwap(current, old, staged string) error {
	if err := moveFile(current, old); err != nil {
		return err
	}
	if err := moveFile(staged, current); err != nil {
		_ = moveFile(old, current)
		return err
	}
	return nil
}

func replaceFile(source, destination string) error {
	return moveFile(source, destination)
}
