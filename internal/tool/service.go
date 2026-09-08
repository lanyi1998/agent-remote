package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"agent-remote/internal/runtimebundle"
)

type ChunkWriter func([]byte)

type ServiceConfig struct {
	Root              string
	AllowOutsideRoot  bool
	RuntimeDir        string
	BashPath          string
	BusyBoxPath       string
	MaxReadBytes      int
	MaxOutputBytes    int
	DefaultTimeoutSec int
	MaxTimeoutSec     int
}

type Service struct {
	paths             *PathResolver
	runtime           *runtimebundle.Resolver
	maxReadBytes      int
	maxOutputBytes    int
	defaultTimeoutSec int
	maxTimeoutSec     int
	mutationLocks     sync.Map
}

func NewService(config ServiceConfig) (*Service, error) {
	paths, err := NewPathResolver(config.Root, config.AllowOutsideRoot)
	if err != nil {
		return nil, err
	}
	applyServiceDefaults(&config)
	return &Service{
		paths:             paths,
		runtime:           runtimebundle.NewResolver(config.RuntimeDir, config.BashPath, config.BusyBoxPath),
		maxReadBytes:      config.MaxReadBytes,
		maxOutputBytes:    config.MaxOutputBytes,
		defaultTimeoutSec: config.DefaultTimeoutSec,
		maxTimeoutSec:     config.MaxTimeoutSec,
	}, nil
}

func applyServiceDefaults(config *ServiceConfig) {
	if config.MaxReadBytes <= 0 {
		config.MaxReadBytes = 256 * 1024
	}
	if config.MaxOutputBytes <= 0 {
		config.MaxOutputBytes = 256 * 1024
	}
	if config.DefaultTimeoutSec <= 0 {
		config.DefaultTimeoutSec = 120
	}
	if config.MaxTimeoutSec <= 0 {
		config.MaxTimeoutSec = 3600
	}
}

func (s *Service) Root() string {
	return s.paths.Root()
}

func (s *Service) Execute(ctx context.Context, name string, input json.RawMessage, onChunk ChunkWriter) (json.RawMessage, error) {
	return s.ExecuteWithInput(ctx, name, input, nil, onChunk)
}

func (s *Service) ExecuteWithInput(ctx context.Context, name string, input json.RawMessage, inputReader io.Reader, onChunk ChunkWriter) (json.RawMessage, error) {
	var result interface{}
	var err error
	switch name {
	case "read":
		result, err = s.executeRead(input)
	case "bash":
		result, err = s.executeBash(ctx, input, inputReader, onChunk)
	case "edit":
		result, err = s.executeEdit(input)
	case "write":
		result, err = s.executeWrite(input)
	case "find":
		result, err = s.executeFind(input)
	default:
		return nil, NewError("unknown_tool", fmt.Sprintf("unknown tool %q", name))
	}
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, WrapError("encode_result", "encode tool result", err)
	}
	return encoded, nil
}

func decodeInput(input json.RawMessage, destination interface{}) error {
	if len(input) == 0 {
		return NewError("invalid_input", "tool input is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return WrapError("invalid_input", "decode tool input", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return NewError("invalid_input", "tool input must contain exactly one JSON value")
	}
	return nil
}

func (s *Service) withMutationLock(path string, operation func() error) error {
	value, _ := s.mutationLocks.LoadOrStore(path, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	return operation()
}
