package tool

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxReadLines       = 2000
	binaryDetectBuffer = 32 * 1024
)

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
	file, err := os.Open(absolute)
	if err != nil {
		return readResult{}, WrapError("read_failed", fmt.Sprintf("open %s", input.Path), err)
	}
	defer file.Close()
	if isBinaryFile(file) {
		return s.readBinaryResult(input.Path, absolute, file)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return readResult{}, WrapError("read_failed", fmt.Sprintf("rewind %s", input.Path), err)
	}
	return s.readTextResult(input, file)
}

func isBinaryFile(file *os.File) bool {
	buffer := make([]byte, binaryDetectBuffer+utf8.UTFMax)
	pending := 0
	for {
		read, err := file.Read(buffer[pending:binaryDetectBuffer])
		data := buffer[:pending+read]
		for len(data) > 0 {
			if data[0] == 0 {
				return true
			}
			if !utf8.FullRune(data) && err == nil {
				copy(buffer, data)
				pending = len(data)
				break
			}
			_, size := utf8.DecodeRune(data)
			if size == 1 && data[0] >= utf8.RuneSelf {
				return true
			}
			data = data[size:]
			pending = 0
		}
		if err == io.EOF {
			return pending != 0
		}
		if err != nil {
			return true
		}
	}
}

func (s *Service) readBinaryResult(displayPath, absolute string, file *os.File) (readResult, error) {
	info, err := file.Stat()
	if err != nil {
		return readResult{}, WrapError("read_failed", fmt.Sprintf("stat %s", displayPath), err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return readResult{}, WrapError("read_failed", fmt.Sprintf("rewind %s", displayPath), err)
	}
	maxInputBytes := s.maxReadBytes / 4 * 3
	content, err := io.ReadAll(io.LimitReader(file, int64(maxInputBytes)))
	if err != nil {
		return readResult{}, WrapError("read_failed", fmt.Sprintf("read %s", displayPath), err)
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(absolute)))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return readResult{
		Path:          displayPath,
		ContentBase64: base64.StdEncoding.EncodeToString(content),
		Encoding:      "base64",
		MIMEType:      mimeType,
		Truncated:     info.Size() > int64(len(content)),
	}, nil
}

func (s *Service) readTextResult(input readInput, file io.Reader) (readResult, error) {
	startLine := 1
	if input.Offset != nil {
		startLine = *input.Offset
	}
	requestedLines := maxReadLines
	if input.Limit != nil && *input.Limit < requestedLines {
		requestedLines = *input.Limit
	}
	reader := bufio.NewReader(file)
	content := bytes.Buffer{}
	totalLines, outputLines, lineNumber := 1, 0, 1
	selectionFinished := false
	for {
		line, ended, reachedEOF, err := readLineWithinBytes(reader, s.maxReadBytes)
		if err != nil {
			return readResult{}, WrapError("read_failed", fmt.Sprintf("read %s", input.Path), err)
		}
		if lineNumber >= startLine && lineNumber < startLine+requestedLines && !selectionFinished {
			separatorBytes := 0
			if outputLines > 0 {
				separatorBytes = 1
			}
			if line.totalBytes+separatorBytes <= s.maxReadBytes-content.Len() {
				if outputLines > 0 {
					content.WriteByte('\n')
				}
				content.Write(line.prefix)
				outputLines++
			} else if outputLines == 0 {
				content.WriteString(truncateUTF8(string(line.prefix), s.maxReadBytes))
				content.WriteString("\n\n[First line was truncated by the byte limit.]")
				outputLines++
				selectionFinished = true
			} else {
				selectionFinished = true
			}
		}
		if ended {
			totalLines++
			lineNumber++
		}
		if reachedEOF {
			break
		}
	}
	if startLine > totalLines {
		return readResult{}, NewError("offset_out_of_range", fmt.Sprintf("offset %d is beyond end of file (%d lines total)", startLine, totalLines))
	}
	endLine := startLine + outputLines - 1
	result := readResult{Path: input.Path, Content: content.String(), Encoding: "utf-8", StartLine: startLine, EndLine: endLine, TotalLines: totalLines, Truncated: endLine < totalLines}
	if result.Truncated {
		result.NextOffset = endLine + 1
		result.Content += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", startLine, endLine, totalLines, result.NextOffset)
	}
	return result, nil
}

type boundedLine struct {
	prefix     []byte
	totalBytes int
}

func readLineWithinBytes(reader *bufio.Reader, maxBytes int) (boundedLine, bool, bool, error) {
	line := boundedLine{prefix: make([]byte, 0, maxBytes)}
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if fragment[len(fragment)-1] == '\n' {
				fragment = fragment[:len(fragment)-1]
				line.totalBytes += len(fragment)
				appendLinePrefix(&line.prefix, fragment, maxBytes)
				return line, true, false, nil
			}
			line.totalBytes += len(fragment)
			appendLinePrefix(&line.prefix, fragment, maxBytes)
		}
		switch err {
		case nil, bufio.ErrBufferFull:
			continue
		case io.EOF:
			return line, false, true, nil
		default:
			return boundedLine{}, false, false, err
		}
	}
}

func appendLinePrefix(prefix *[]byte, fragment []byte, maxBytes int) {
	available := maxBytes - len(*prefix)
	if available <= 0 {
		return
	}
	if len(fragment) > available {
		fragment = fragment[:available]
	}
	*prefix = append(*prefix, fragment...)
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
