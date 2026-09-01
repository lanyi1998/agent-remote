package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathResolverRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	resolver, err := NewPathResolver(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(filepath.Join("..", "outside.txt"), true); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestPathResolverRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	resolver, err := NewPathResolver(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(filepath.Join("escape", "file.txt"), true); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}
