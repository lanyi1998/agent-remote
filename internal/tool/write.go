package tool

import (
	"fmt"
	"os"
	"path/filepath"
)

type writeInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type writeResult struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	Message string `json:"message"`
}

func (s *Service) executeWrite(rawInput []byte) (writeResult, error) {
	var input writeInput
	if err := decodeInput(rawInput, &input); err != nil {
		return writeResult{}, err
	}
	absolute, err := s.paths.Resolve(input.Path, true)
	if err != nil {
		return writeResult{}, err
	}
	result := writeResult{}
	err = s.withMutationLock(absolute, func() error {
		if err := os.MkdirAll(filepath.Dir(absolute), 0755); err != nil {
			return WrapError("mkdir_failed", "create parent directories", err)
		}
		if err := os.WriteFile(absolute, []byte(input.Content), 0644); err != nil {
			return WrapError("write_failed", fmt.Sprintf("write %s", input.Path), err)
		}
		byteCount := len([]byte(input.Content))
		result = writeResult{
			Path:    input.Path,
			Bytes:   byteCount,
			Message: fmt.Sprintf("Successfully wrote %d bytes to %s", byteCount, input.Path),
		}
		return nil
	})
	return result, err
}
