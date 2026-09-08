package connectionstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositorySavesAndLoadsActiveConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections", "config.json")
	repository := New(path)
	store, connection, err := Empty().UpsertActive("http://gateway.example:8787/", "shared-token", "office", "Office worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(store); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Load()
	if err != nil {
		t.Fatal(err)
	}
	active, ok := loaded.ActiveConnection()
	if !ok || active != connection {
		t.Fatalf("active connection = %#v, %t; want %#v", active, ok, connection)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestUpsertActivePreservesConnectionID(t *testing.T) {
	store, first, err := Empty().UpsertActive("http://gateway.example:8787", "shared-token", "remote", "Development")
	if err != nil {
		t.Fatal(err)
	}
	updated, second, err := store.UpsertActive("http://gateway.example:8787", "new-shared-token", "remote", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Note != "Development" || second.Token != "new-shared-token" {
		t.Fatalf("updated connection = %#v", second)
	}
	if len(updated.Connections) != 1 || updated.Active != first.ID {
		t.Fatalf("updated store = %#v", updated)
	}
}

func TestNormalizeURLRejectsPaths(t *testing.T) {
	if _, err := NormalizeURL("http://gateway.example:8787/v1/rpc"); err == nil {
		t.Fatal("expected path validation error")
	}
}
