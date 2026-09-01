package runtimebundle

import (
	"path/filepath"
	"testing"
)

func TestRuntimeRootFromBash(t *testing.T) {
	bash := filepath.Join("C:", "PortableGit", "usr", "bin", "bash.exe")
	expected := filepath.Join("C:", "PortableGit")
	if actual := runtimeRootFromBash(bash); actual != expected {
		t.Fatalf("runtime root = %q, want %q", actual, expected)
	}
}
