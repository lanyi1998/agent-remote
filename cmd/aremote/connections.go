package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	remoteclient "agent-remote/internal/client"
	"agent-remote/internal/connectionstore"
	"agent-remote/internal/protocol"
	"golang.org/x/term"
)

type connectionView struct {
	ID     string          `json:"id"`
	URL    string          `json:"url"`
	Target string          `json:"target"`
	Note   string          `json:"note,omitempty"`
	Active bool            `json:"active"`
	Online bool            `json:"online"`
	Error  string          `json:"error,omitempty"`
	Info   *protocol.Hello `json:"info,omitempty"`
}

var errNoActiveConnection = errors.New("no active connection; run 'aremote connect URL TOKEN' first")

func activeConnection() (connectionstore.Connection, error) {
	repository, err := connectionRepository()
	if err != nil {
		return connectionstore.Connection{}, err
	}
	store, err := repository.Load()
	if err != nil {
		return connectionstore.Connection{}, err
	}
	connection, ok := store.ActiveConnection()
	if !ok {
		return connectionstore.Connection{}, errNoActiveConnection
	}
	return connection, nil
}

func connectionRepository() (connectionstore.Repository, error) {
	path, err := connectionstore.DefaultPath()
	if err != nil {
		return connectionstore.Repository{}, err
	}
	return connectionstore.New(path), nil
}

func optionsForConnection(options globalOptions, connection connectionstore.Connection) globalOptions {
	options.url = connection.URL
	options.token = connection.Token
	options.target = connection.Target
	return options
}

func runConnect(ctx context.Context, arguments []string, output io.Writer) error {
	url, token, target, note, err := parseConnectArguments(arguments)
	if err != nil {
		return err
	}
	connection, err := connectionFor(url, token, target, note)
	if err != nil {
		return err
	}
	info, err := verifyTarget(ctx, connection)
	if err != nil {
		return err
	}
	repository, err := connectionRepository()
	if err != nil {
		return err
	}
	store, err := repository.Load()
	if err != nil {
		return err
	}
	store, saved, err := store.UpsertActive(connection.URL, connection.Token, connection.Target, connection.Note)
	if err != nil {
		return err
	}
	if err := repository.Save(store); err != nil {
		return err
	}
	return writeJSON(output, connectedView(saved, true, info, ""))
}

func runStatus(ctx context.Context, output io.Writer) error {
	connection, err := activeConnection()
	if errors.Is(err, errNoActiveConnection) {
		return writeJSON(output, map[string]bool{"connected": false})
	}
	if err != nil {
		return err
	}
	return writeJSON(output, inspectConnection(ctx, connection, true))
}

func runList(ctx context.Context, ioStreams streams) error {
	repository, err := connectionRepository()
	if err != nil {
		return err
	}
	store, err := repository.Load()
	if err != nil {
		return err
	}
	views := make([]connectionView, 0, len(store.Connections))
	for _, connection := range store.Connections {
		views = append(views, inspectConnection(ctx, connection, connection.ID == store.Active))
	}
	if !isInteractiveInput(ioStreams.in) {
		return writeJSON(ioStreams.out, map[string]interface{}{"connections": views})
	}
	return selectConnection(ioStreams, repository, store, views)
}

func runRemove(arguments []string, ioStreams streams) error {
	if len(arguments) > 1 {
		return errors.New("usage: aremote remove [CONNECTION_ID]")
	}
	repository, err := connectionRepository()
	if err != nil {
		return err
	}
	store, err := repository.Load()
	if err != nil {
		return err
	}
	id := ""
	if len(arguments) == 1 {
		id = arguments[0]
	}
	if id == "" {
		if !isInteractiveInput(ioStreams.in) {
			return errors.New("usage: aremote remove CONNECTION_ID")
		}
		id, err = selectConnectionToRemove(ioStreams, store)
		if err != nil {
			return err
		}
	}
	updated, removed := store.Remove(id)
	if !removed {
		return fmt.Errorf("saved connection %q does not exist", id)
	}
	if err := repository.Save(updated); err != nil {
		return err
	}
	return writeJSON(ioStreams.out, map[string]string{"removed": id})
}

func runRefresh(ctx context.Context, output io.Writer) error {
	connection, err := activeConnection()
	if err != nil {
		return err
	}
	return writeJSON(output, inspectConnection(ctx, connection, true))
}

func parseConnectArguments(arguments []string) (string, string, string, string, error) {
	if len(arguments) < 2 {
		return "", "", "", "", errors.New("usage: aremote connect URL TOKEN [--worker ID] [NOTE...]")
	}
	target := "remote"
	noteArguments := arguments[2:]
	if len(noteArguments) > 0 && noteArguments[0] == "--worker" {
		if len(noteArguments) < 2 || noteArguments[1] == "" {
			return "", "", "", "", errors.New("usage: aremote connect URL TOKEN [--worker ID] [NOTE...]")
		}
		target = noteArguments[1]
		noteArguments = noteArguments[2:]
	}
	return arguments[0], arguments[1], target, joinArguments(noteArguments), nil
}

func connectionFor(url, token, target, note string) (connectionstore.Connection, error) {
	normalizedURL, err := connectionstore.NormalizeURL(url)
	if err != nil {
		return connectionstore.Connection{}, err
	}
	return connectionstore.Connection{URL: normalizedURL, Token: token, Target: target, Note: note}, nil
}

func verifyTarget(ctx context.Context, connection connectionstore.Connection) (*protocol.Hello, error) {
	client, err := remoteclient.New(remoteclient.Config{BaseURL: connection.URL, Token: connection.Token})
	if err != nil {
		return nil, err
	}
	targets, err := client.Targets(ctx)
	if err != nil {
		return nil, err
	}
	if connection.Target == "remote" && targets.Remote {
		return &targets.RemoteInfo, nil
	}
	for _, worker := range targets.Workers {
		if worker.WorkerID == connection.Target {
			return &worker, nil
		}
	}
	return nil, fmt.Errorf("target %q is not online", connection.Target)
}

func inspectConnection(ctx context.Context, connection connectionstore.Connection, active bool) connectionView {
	info, err := verifyTarget(ctx, connection)
	if err != nil {
		return connectedView(connection, false, nil, err.Error())
	}
	return connectedView(connection, active, info, "")
}

func connectedView(connection connectionstore.Connection, active bool, info *protocol.Hello, message string) connectionView {
	return connectionView{
		ID:     connection.ID,
		URL:    connection.URL,
		Target: connection.Target,
		Note:   connection.Note,
		Active: active,
		Online: message == "",
		Error:  message,
		Info:   info,
	}
}

func joinArguments(arguments []string) string {
	if len(arguments) == 0 {
		return ""
	}
	result := arguments[0]
	for _, argument := range arguments[1:] {
		result += " " + argument
	}
	return result
}

func isInteractiveInput(input io.Reader) bool {
	file, ok := input.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func selectConnection(ioStreams streams, repository connectionstore.Repository, store connectionstore.Store, views []connectionView) error {
	if len(views) == 0 {
		_, err := fmt.Fprintln(ioStreams.out, "No saved connections")
		return err
	}
	for index, view := range views {
		marker := " "
		if view.Active {
			marker = "*"
		}
		availability := "online"
		if !view.Online {
			availability = "offline: " + view.Error
		}
		note := view.Note
		if note == "" {
			note = "(unnamed remote)"
		}
		if _, err := fmt.Fprintf(ioStreams.out, "%d. %s %s — %s @ %s — %s\n", index+1, marker, note, view.Target, view.URL, availability); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(ioStreams.out, "Select a connection (Enter keeps the current target): "); err != nil {
		return err
	}
	input := bufio.NewReader(ioStreams.in)
	selected, err := input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read connection selection: %w", err)
	}
	if selected = strings.TrimSpace(selected); selected == "" {
		return nil
	}
	index, err := strconv.Atoi(selected)
	if err != nil || index < 1 || index > len(views) {
		return errors.New("connection selection must be a listed number")
	}
	view := views[index-1]
	if !view.Online {
		return fmt.Errorf("selected connection is offline: %s", view.Error)
	}
	store.Active = view.ID
	if err := repository.Save(store); err != nil {
		return err
	}
	_, err = fmt.Fprintf(ioStreams.out, "Selected %s\n", view.ID)
	return err
}

func selectConnectionToRemove(ioStreams streams, store connectionstore.Store) (string, error) {
	if len(store.Connections) == 0 {
		return "", errors.New("no saved connection to remove")
	}
	for index, connection := range store.Connections {
		note := connection.Note
		if note == "" {
			note = "(unnamed remote)"
		}
		if _, err := fmt.Fprintf(ioStreams.out, "%d. %s — %s @ %s [%s]\n", index+1, note, connection.Target, connection.URL, connection.ID); err != nil {
			return "", err
		}
	}
	if _, err := fmt.Fprint(ioStreams.out, "Remove connection number (Enter cancels): "); err != nil {
		return "", err
	}
	input := bufio.NewReader(ioStreams.in)
	selected, err := input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read connection selection: %w", err)
	}
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return "", errors.New("connection removal cancelled")
	}
	index, err := strconv.Atoi(selected)
	if err != nil || index < 1 || index > len(store.Connections) {
		return "", errors.New("connection selection must be a listed number")
	}
	if _, err := fmt.Fprint(ioStreams.out, "Confirm removal (y/N): "); err != nil {
		return "", err
	}
	confirmed, err := input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read removal confirmation: %w", err)
	}
	if strings.TrimSpace(strings.ToLower(confirmed)) != "y" {
		return "", errors.New("connection removal cancelled")
	}
	return store.Connections[index-1].ID, nil
}
