package tool

import (
	"encoding/base64"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxReadLines = 2000

type readInput struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
}

type readResult struct {
	Path          string `json:"path"`
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	Encoding      string `json:"encoding"`
	MIMEType      string `json:"mime_type,omitempty"`
	StartLine     int    `json:"start_line,omitempty"`
	EndLine       int    `json:"end_line,omitempty"`
	TotalLines    int    `json:"total_lines,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	NextOffset    int    `json:"next_offset,omitempty"`
}

func (s *Service) executeRead(rawInput []byte) (readResult, error) {
	var input readInput
	if err := decodeInput(rawInput, &input); err != nil {
		return readResult{}, err
	}
	if input.Offset != nil && *input.Offset < 1 {
		return readResult{}, NewError("invalid_input", "offset must be at least 1")
	}
	if input.Limit != nil && *input.Limit < 1 {
		return readResult{}, NewError("invalid_input", "limit must be at least 1")
	}
	absolute, err := s.paths.Resolve(input.Path, false)
	if err != nil {
		return readResult{}, err
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return readResult{}, WrapError("read_failed", fmt.Sprintf("read %s", input.Path), err)
	}
	if isBinary(content) {
		return readBinaryResult(input.Path, absolute, content), nil
	}
	return s.readTextResult(input, string(content))
}

func isBinary(content []byte) bool {
	return !utf8.Valid(content) || strings.IndexByte(string(content), 0) >= 0
}

func readBinaryResult(displayPath, absolute string, content []byte) readResult {
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(absolute)))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return readResult{
		Path:          displayPath,
		ContentBase64: base64.StdEncoding.EncodeToString(content),
		Encoding:      "base64",
		MIMEType:      mimeType,
	}
}

func (s *Service) readTextResult(input readInput, content string) (readResult, error) {
	lines := strings.Split(content, "\n")
	start := 0
	if input.Offset != nil {
		start = *input.Offset - 1
	}
	if start >= len(lines) {
		return readResult{}, NewError("offset_out_of_range", fmt.Sprintf("offset %d is beyond end of file (%d lines total)", start+1, len(lines)))
	}
	requestedEnd := len(lines)
	if input.Limit != nil && start+*input.Limit < requestedEnd {
		requestedEnd = start + *input.Limit
	}
	end := requestedEnd
	if end-start > maxReadLines {
		end = start + maxReadLines
	}
	selected, outputLines := fitLinesWithinBytes(lines[start:end], s.maxReadBytes)
	actualEnd := start + outputLines
	result := readResult{
		Path:       input.Path,
		Content:    selected,
		Encoding:   "utf-8",
		StartLine:  start + 1,
		EndLine:    actualEnd,
		TotalLines: len(lines),
		Truncated:  actualEnd < len(lines),
	}
	if actualEnd < len(lines) {
		result.NextOffset = actualEnd + 1
		result.Content += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", start+1, actualEnd, len(lines), result.NextOffset)
	}
	return result, nil
}

func fitLinesWithinBytes(lines []string, maxBytes int) (string, int) {
	if len(lines) == 0 {
		return "", 0
	}
	total := 0
	count := 0
	for index, line := range lines {
		lineBytes := len([]byte(line))
		separatorBytes := 0
		if index > 0 {
			separatorBytes = 1
		}
		if total+separatorBytes+lineBytes > maxBytes {
			break
		}
		total += separatorBytes + lineBytes
		count++
	}
	if count == 0 {
		prefix := truncateUTF8(lines[0], maxBytes)
		return prefix + "\n\n[First line was truncated by the byte limit.]", 1
	}
	return strings.Join(lines[:count], "\n"), count
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}
