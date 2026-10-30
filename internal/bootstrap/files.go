package bootstrap

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

func openDirectory(dir string) (*os.Root, *os.File, error) {
	if dir == "" {
		return nil, nil, errors.New("config directory is required")
	}
	// Trailing separators or /. must not make Lstat follow the final symlink.
	dir = filepath.Clean(dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, errors.New("cannot create config directory")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, nil, errors.New("config directory must be a real directory with mode 0700")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, errors.New("cannot open config directory")
	}
	if info, err := root.Lstat(".init.lock"); err == nil && !info.Mode().IsRegular() {
		root.Close()
		return nil, nil, errors.New("init lock must be a regular file")
	}
	lock, err := root.OpenFile(".init.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		root.Close()
		return nil, nil, errors.New("cannot open init lock")
	}
	info, err = lock.Stat()
	if err != nil || !info.Mode().IsRegular() {
		lock.Close()
		root.Close()
		return nil, nil, errors.New("init lock must be a regular file")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		root.Close()
		return nil, nil, errors.New("config directory is locked by another init")
	}
	return root, lock, nil
}

func readExisting(root *os.Root, name string) ([]byte, error) {
	before, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", name)
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot safely open %s", name)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return nil, fmt.Errorf("%s must be a regular file", name)
	}
	limit := 4096
	if name == "vless.json" {
		limit = 65536
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit+1)))
	if err != nil || len(raw) > limit {
		return nil, fmt.Errorf("cannot read %s within its size limit", name)
	}
	// Distinguish an existing empty file from an absent file.
	if raw == nil {
		raw = []byte{}
	}
	return raw, nil
}

func publish(root *os.Root, name string, raw []byte) error {
	temp := ".init-" + randomHex(16)
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("cannot prepare %s", name)
	}
	defer root.Remove(temp)
	defer f.Close()
	if _, err := f.Write(raw); err != nil {
		return fmt.Errorf("cannot write %s", name)
	}
	// The parent is private; bind-mounted Compose secrets need to be readable by UID 10001.
	if err := f.Chmod(0644); err != nil {
		return fmt.Errorf("cannot set permissions for %s", name)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("cannot sync %s", name)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot close %s", name)
	}
	// Link publishes the fully written inode and fails if a destination already exists.
	if err := root.Link(temp, name); err != nil {
		return fmt.Errorf("cannot publish %s without replacing an existing file", name)
	}
	directory, err := root.Open(".")
	if err != nil {
		return errors.New("cannot open config directory for sync")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("cannot sync config directory")
	}
	return nil
}
