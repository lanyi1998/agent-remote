package connectionstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const storeVersion = 1

type Connection struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Target string `json:"worker"`
	Note   string `json:"note"`
	Token  string `json:"token"`
}

type Store struct {
	Version     int          `json:"version"`
	Active      string       `json:"active,omitempty"`
	Connections []Connection `json:"targets"`
}

type Repository struct {
	path string
}

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	return filepath.Join(home, ".agent-remote", "config.json"), nil
}

func New(path string) Repository {
	return Repository{path: path}
}

func (r Repository) Load() (Store, error) {
	contents, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return Empty(), nil
	}
	if err != nil {
		return Store{}, fmt.Errorf("read connection configuration: %w", err)
	}
	store, err := decodeStore(contents)
	if err != nil {
		return Store{}, fmt.Errorf("decode connection configuration: %w", err)
	}
	if err := validate(store); err != nil {
		return Store{}, fmt.Errorf("validate connection configuration: %w", err)
	}
	return store, nil
}

func decodeStore(contents []byte) (Store, error) {
	var persisted struct {
		Version     int                `json:"version"`
		Active      string             `json:"active"`
		Targets     []Connection       `json:"targets"`
		Connections []legacyConnection `json:"connections"`
	}
	if err := json.Unmarshal(contents, &persisted); err != nil {
		return Store{}, err
	}
	if persisted.Targets != nil {
		return Store{Version: persisted.Version, Active: persisted.Active, Connections: persisted.Targets}, nil
	}
	connections := make([]Connection, 0, len(persisted.Connections))
	for _, connection := range persisted.Connections {
		connections = append(connections, Connection{
			ID: connection.ID, URL: connection.URL, Target: connection.Target,
			Note: connection.Note, Token: connection.Token,
		})
	}
	return Store{Version: persisted.Version, Active: persisted.Active, Connections: connections}, nil
}

type legacyConnection struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Target string `json:"target"`
	Note   string `json:"note"`
	Token  string `json:"token"`
}

func (r Repository) Save(store Store) error {
	if err := validate(store); err != nil {
		return fmt.Errorf("validate connection configuration: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("create connection configuration directory: %w", err)
	}
	contents, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("encode connection configuration: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(r.path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary connection configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set connection configuration permissions: %w", err)
	}
	if _, err := temporary.Write(append(contents, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("write connection configuration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close connection configuration: %w", err)
	}
	if err := os.Rename(temporaryPath, r.path); err != nil {
		return fmt.Errorf("replace connection configuration: %w", err)
	}
	return nil
}

func Empty() Store {
	return Store{Version: storeVersion, Connections: []Connection{}}
}

func (s Store) ActiveConnection() (Connection, bool) {
	return s.Connection(s.Active)
}

func (s Store) Connection(id string) (Connection, bool) {
	for _, connection := range s.Connections {
		if connection.ID == id {
			return connection, true
		}
	}
	return Connection{}, false
}

func (s Store) UpsertActive(url, token, target, note string) (Store, Connection, error) {
	connection, err := newConnection(url, token, target, note)
	if err != nil {
		return Store{}, Connection{}, err
	}
	connections := append([]Connection(nil), s.Connections...)
	for index, existing := range connections {
		if existing.URL == connection.URL && existing.Target == connection.Target {
			connection.ID = existing.ID
			if connection.Note == "" {
				connection.Note = existing.Note
			}
			connections[index] = connection
			return Store{Version: storeVersion, Active: connection.ID, Connections: connections}, connection, nil
		}
	}
	connections = append(connections, connection)
	return Store{Version: storeVersion, Active: connection.ID, Connections: connections}, connection, nil
}

func (s Store) Remove(id string) (Store, bool) {
	connections := make([]Connection, 0, len(s.Connections))
	removed := false
	for _, connection := range s.Connections {
		if connection.ID == id {
			removed = true
			continue
		}
		connections = append(connections, connection)
	}
	active := s.Active
	if active == id {
		active = ""
	}
	return Store{Version: storeVersion, Active: active, Connections: connections}, removed
}

func NormalizeURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("remote URL is required")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse remote URL: %w", err)
	}
	if parsed.Scheme != "http" {
		return "", errors.New("remote URL must use http")
	}
	if parsed.Host == "" {
		return "", errors.New("remote URL must include a host")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("remote URL must not include a path")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("remote URL must not include a query or fragment")
	}
	parsed.Path = ""
	return parsed.String(), nil
}

func validate(store Store) error {
	if store.Version != storeVersion {
		return fmt.Errorf("expected version %d", storeVersion)
	}
	ids := make(map[string]struct{}, len(store.Connections))
	activeFound := store.Active == ""
	for index, connection := range store.Connections {
		if err := validateConnection(connection); err != nil {
			return fmt.Errorf("connection %d: %w", index, err)
		}
		if _, exists := ids[connection.ID]; exists {
			return fmt.Errorf("connection %d: duplicate id %q", index, connection.ID)
		}
		ids[connection.ID] = struct{}{}
		activeFound = activeFound || connection.ID == store.Active
	}
	if !activeFound {
		return errors.New("active connection does not exist")
	}
	return nil
}

func newConnection(url, token, target, note string) (Connection, error) {
	normalizedURL, err := NormalizeURL(url)
	if err != nil {
		return Connection{}, err
	}
	if err := validateTokenAndTarget(token, target); err != nil {
		return Connection{}, err
	}
	target = strings.TrimSpace(target)
	id, err := newID()
	if err != nil {
		return Connection{}, err
	}
	return Connection{ID: id, URL: normalizedURL, Token: token, Target: target, Note: strings.TrimSpace(note)}, nil
}

func validateConnection(connection Connection) error {
	if strings.TrimSpace(connection.ID) == "" {
		return errors.New("id is required")
	}
	if _, err := NormalizeURL(connection.URL); err != nil {
		return err
	}
	if err := validateTokenAndTarget(connection.Token, connection.Target); err != nil {
		return err
	}
	return nil
}

func validateTokenAndTarget(token, target string) error {
	if len(token) < 8 {
		return errors.New("a shared token of at least 8 characters is required")
	}
	target = strings.TrimSpace(target)
	if target == "" || target == "off" {
		return errors.New("target must be remote or a Worker ID")
	}
	return nil
}

func newID() (string, error) {
	value := make([]byte, 4)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate connection id: %w", err)
	}
	return "target-" + hex.EncodeToString(value), nil
}
