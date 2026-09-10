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

type targetView struct {
	ID     string          `json:"id"`
	URL    string          `json:"url"`
	Worker string          `json:"worker"`
	Note   string          `json:"note,omitempty"`
	Active bool            `json:"active"`
	Online bool            `json:"online"`
	Error  string          `json:"error,omitempty"`
	Info   *protocol.Hello `json:"info,omitempty"`
}

var errNoActiveTarget = errors.New("no active target; run 'aremote connect URL TOKEN' first")

func activeConnection() (connectionstore.Connection, error) {
	return selectedConnection("")
}

func selectedConnection(id string) (connectionstore.Connection, error) {
	repository, err := connectionRepository()
	if err != nil {
		return connectionstore.Connection{}, err
	}
	store, err := repository.Load()
	if err != nil {
		return connectionstore.Connection{}, err
	}
	if id != "" {
		connection, ok := store.Connection(id)
		if !ok {
			return connectionstore.Connection{}, fmt.Errorf("saved target %q does not exist", id)
		}
		return connection, nil
	}
	connection, ok := store.ActiveConnection()
	if !ok {
		return connectionstore.Connection{}, errNoActiveTarget
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
	options.worker = connection.Target
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
	if errors.Is(err, errNoActiveTarget) {
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
	views := make([]targetView, 0, len(store.Connections))
	for _, connection := range store.Connections {
		views = append(views, inspectConnection(ctx, connection, connection.ID == store.Active))
	}
	if !isInteractiveInput(ioStreams.in) {
		return writeJSON(ioStreams.out, map[string]interface{}{"targets": views})
	}
	return selectConnection(ctx, ioStreams, repository, store, views)
}

func runRemove(ctx context.Context, arguments []string, ioStreams streams) error {
	if len(arguments) > 1 {
		return errors.New("usage: aremote remove [TARGET_ID]")
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
			return errors.New("usage: aremote remove TARGET_ID")
		}
		id, err = selectConnectionToRemove(ctx, ioStreams, store)
		if err != nil {
			return err
		}
	}
	updated, removed := store.Remove(id)
	if !removed {
		return fmt.Errorf("saved target %q does not exist", id)
	}
	if err := repository.Save(updated); err != nil {
		return err
	}
	return writeJSON(ioStreams.out, map[string]string{"removed": id})
}

func runRename(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("usage: aremote rename NOTE...")
	}
	repository, err := connectionRepository()
	if err != nil {
		return err
	}
	store, err := repository.Load()
	if err != nil {
		return err
	}
	if store.Active == "" {
		return errNoActiveTarget
	}
	updated, target, renamed := store.Rename(store.Active, joinArguments(arguments))
	if !renamed {
		return errNoActiveTarget
	}
	if err := repository.Save(updated); err != nil {
		return err
	}
	return writeJSON(output, map[string]string{"id": target.ID, "note": target.Note})
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

func inspectConnection(ctx context.Context, connection connectionstore.Connection, active bool) targetView {
	info, err := verifyTarget(ctx, connection)
	if err != nil {
		return connectedView(connection, false, nil, err.Error())
	}
	return connectedView(connection, active, info, "")
}

func connectedView(connection connectionstore.Connection, active bool, info *protocol.Hello, message string) targetView {
	return targetView{
		ID:     connection.ID,
		URL:    connection.URL,
		Worker: connection.Target,
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

func selectConnection(ctx context.Context, ioStreams streams, repository connectionstore.Repository, store connectionstore.Store, views []targetView) error {
	if len(views) == 0 {
		_, err := fmt.Fprintln(ioStreams.out, "No saved targets")
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
		if _, err := fmt.Fprintf(ioStreams.out, "%d. %s %s — %s @ %s [%s] — %s\n", index+1, marker, note, view.Worker, view.URL, view.ID, availability); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(ioStreams.out, "Select a target (Enter keeps the current target): "); err != nil {
		return err
	}
	input := bufio.NewReader(ioStreams.in)
	selected, err := readSelection(ctx, input)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read connection selection: %w", err)
	}
	if selected = strings.TrimSpace(selected); selected == "" {
		return nil
	}
	index, err := strconv.Atoi(selected)
	if err != nil || index < 1 || index > len(views) {
		return errors.New("target selection must be a listed number")
	}
	view := views[index-1]
	if !view.Online {
		return fmt.Errorf("selected target is offline: %s", view.Error)
	}
	store.Active = view.ID
	if err := repository.Save(store); err != nil {
		return err
	}
	_, err = fmt.Fprintf(ioStreams.out, "Selected target %s\n", view.ID)
	return err
}

func selectConnectionToRemove(ctx context.Context, ioStreams streams, store connectionstore.Store) (string, error) {
	if len(store.Connections) == 0 {
		return "", errors.New("no saved target to remove")
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
	if _, err := fmt.Fprint(ioStreams.out, "Remove target number (Enter cancels): "); err != nil {
		return "", err
	}
	input := bufio.NewReader(ioStreams.in)
	selected, err := readSelection(ctx, input)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read target selection: %w", err)
	}
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return "", errors.New("target removal cancelled")
	}
	index, err := strconv.Atoi(selected)
	if err != nil || index < 1 || index > len(store.Connections) {
		return "", errors.New("target selection must be a listed number")
	}
	if _, err := fmt.Fprint(ioStreams.out, "Confirm removal (y/N): "); err != nil {
		return "", err
	}
	confirmed, err := readSelection(ctx, input)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read removal confirmation: %w", err)
	}
	if strings.TrimSpace(strings.ToLower(confirmed)) != "y" {
		return "", errors.New("target removal cancelled")
	}
	return store.Connections[index-1].ID, nil
}

type selectionReadResult struct {
	value string
	err   error
}

func readSelection(ctx context.Context, input *bufio.Reader) (string, error) {
	result := make(chan selectionReadResult, 1)
	go func() {
		var selected strings.Builder
		for {
			character, _, err := input.ReadRune()
			if err != nil {
				result <- selectionReadResult{value: selected.String(), err: err}
				return
			}
			if character == '\x03' {
				result <- selectionReadResult{err: context.Canceled}
				return
			}
			if character == '\n' {
				result <- selectionReadResult{value: selected.String()}
				return
			}
			selected.WriteRune(character)
		}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case read := <-result:
		return read.value, read.err
	}
}
