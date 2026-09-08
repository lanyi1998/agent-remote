package connectionstore

import (
	"encoding/json"
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
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]interface{}
	if err := json.Unmarshal(contents, &persisted); err != nil {
		t.Fatal(err)
	}
	if _, exists := persisted["connections"]; exists {
		t.Fatalf("persisted configuration must use targets: %s", contents)
	}
	targets, ok := persisted["targets"].([]interface{})
	if !ok || len(targets) != 1 {
		t.Fatalf("targets = %#v", persisted["targets"])
	}
	target, ok := targets[0].(map[string]interface{})
	if !ok || target["worker"] != "office" {
		t.Fatalf("target = %#v", targets[0])
	}
}

func TestRepositoryLoadsLegacyConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	contents := []byte(`{"version":1,"active":"connection-1234","connections":[{"id":"connection-1234","url":"http://gateway.example:8787","target":"remote","note":"Legacy","token":"shared-token"}]}`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	active, ok := store.ActiveConnection()
	if !ok || active.Target != "remote" {
		t.Fatalf("active target = %#v, %t", active, ok)
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
	if len(first.ID) < len("target-") || first.ID[:len("target-")] != "target-" {
		t.Fatalf("target ID = %q", first.ID)
	}
}

func TestNormalizeURLRejectsPaths(t *testing.T) {
	if _, err := NormalizeURL("http://gateway.example:8787/v1/rpc"); err == nil {
		t.Fatal("expected path validation error")
	}
}

func TestConnectionFindsSavedConnectionByID(t *testing.T) {
	store, connection, err := Empty().UpsertActive("http://gateway.example:8787", "shared-token", "remote", "Development")
	if err != nil {
		t.Fatal(err)
	}
	found, ok := store.Connection(connection.ID)
	if !ok || found != connection {
		t.Fatalf("connection = %#v, %t; want %#v", found, ok, connection)
	}
}
