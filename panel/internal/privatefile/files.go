package privatefile

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

var ErrProtection = errors.New("state file protection is invalid")

func PrivateDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("state directory must be absolute")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
		return ErrProtection
	}
	return nil
}

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Geteuid())
}

// OpenPrivate refuses symlinks and files accessible by other users.
func OpenPrivate(path string, flags int) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("protected file path must be absolute")
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open protected file directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	file, err := OpenNoFollow(root, filepath.Base(path), flags, 0600)
	if err != nil {
		return nil, fmt.Errorf("open protected file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect protected file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) {
		_ = file.Close()
		return nil, ErrProtection
	}
	return file, nil
}

func ReadKey(path string) ([]byte, error) {
	file, err := OpenPrivate(path, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	key, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("master key must contain exactly 32 bytes")
	}
	return key, nil
}

// CreateKey uses an atomic no-replace link, then syncs the containing directory.
// Interrupted setup may leave a key without a DB; retry reuses that protected key.
func CreateKey(path string) (returnErr error) {
	if !filepath.IsAbs(path) {
		return errors.New("master key path must be absolute")
	}
	if err := PrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".master-key-")
	if err != nil {
		return fmt.Errorf("create master key: %w", err)
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary key: %w", err))
		}
	}()
	defer func() { _ = file.Close() }()
	key := make([]byte, 32)
	defer clear(key)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generate master key: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		return fmt.Errorf("write master key: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync master key: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close master key: %w", err)
	}
	if err := os.Link(file.Name(), path); err != nil {
		return fmt.Errorf("publish master key without replacement: %w", err)
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open key directory: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync key directory: %w", err)
	}
	return nil
}

// LockProcess prevents overlapping service/CLI migrations and duplicate worker
// pools. CLI bootstrap/password reset require the panel service to be stopped.
func LockProcess(directory string) (*os.File, error) {
	if err := PrivateDirectory(directory); err != nil {
		return nil, err
	}
	file, err := OpenPrivate(filepath.Join(directory, "process.lock"), os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("another panel process owns this state directory")
	}
	return file, nil
}
