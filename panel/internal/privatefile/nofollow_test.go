package privatefile

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedPathsRejectRelativeSymlinksWithoutTouchingTargets(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir("nested", 0700); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("nested/value", []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"alias": "nested/value", "parent-alias": "nested", "dangling": "missing"} {
		if err := root.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"alias", "parent-alias/value", "dangling"} {
		file, err := OpenNoFollow(root, path, os.O_RDWR|os.O_CREATE, 0600)
		if err == nil {
			_ = file.Close()
			t.Fatalf("protected path followed %s", path)
		}
	}
	if file, err := OpenPrivate(filepath.Join(directory, "alias"), os.O_RDONLY); err == nil {
		_ = file.Close()
		t.Fatal("private-file boundary followed an in-directory symlink")
	}
	file, err := OpenNoFollow(root, "nested/value", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("regular input refused or target modified: %v", err)
	}
	if _, err := root.Stat("missing"); !os.IsNotExist(err) {
		t.Fatalf("dangling symlink created a target: %v", err)
	}
}
