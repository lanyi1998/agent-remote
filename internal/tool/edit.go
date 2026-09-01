package tool

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

type replacement struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type editInput struct {
	Path  string        `json:"path"`
	Edits []replacement `json:"edits"`
}

type editResult struct {
	Path             string `json:"path"`
	Replacements     int    `json:"replacements"`
	FirstChangedLine int    `json:"first_changed_line,omitempty"`
	Message          string `json:"message"`
}

type locatedReplacement struct {
	start   int
	end     int
	newText string
}

func (s *Service) executeEdit(rawInput []byte) (editResult, error) {
	var input editInput
	if err := decodeInput(rawInput, &input); err != nil {
		return editResult{}, err
	}
	if len(input.Edits) == 0 {
		return editResult{}, NewError("invalid_input", "edits must contain at least one replacement")
	}
	absolute, err := s.paths.Resolve(input.Path, false)
	if err != nil {
		return editResult{}, err
	}
	result := editResult{}
	err = s.withMutationLock(absolute, func() error {
		content, err := os.ReadFile(absolute)
		if err != nil {
			return WrapError("read_failed", fmt.Sprintf("read %s", input.Path), err)
		}
		if !utf8.Valid(content) {
			return NewError("binary_file", "edit only supports UTF-8 text files")
		}
		updated, firstLine, err := applyReplacements(content, input.Edits)
		if err != nil {
			return err
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return WrapError("stat_failed", "read file mode", err)
		}
		if err := os.WriteFile(absolute, updated, info.Mode().Perm()); err != nil {
			return WrapError("write_failed", fmt.Sprintf("write %s", input.Path), err)
		}
		result = editResult{
			Path:             input.Path,
			Replacements:     len(input.Edits),
			FirstChangedLine: firstLine,
			Message:          fmt.Sprintf("Successfully replaced %d block(s) in %s", len(input.Edits), input.Path),
		}
		return nil
	})
	return result, err
}

func applyReplacements(rawContent []byte, edits []replacement) ([]byte, int, error) {
	bom, content := splitUTF8BOM(string(rawContent))
	lineEnding := detectLineEnding(content)
	normalized := normalizeLineEndings(content)
	located := make([]locatedReplacement, 0, len(edits))
	firstLine := 0
	for index, edit := range edits {
		oldText := normalizeLineEndings(edit.OldText)
		newText := normalizeLineEndings(edit.NewText)
		if oldText == "" {
			return nil, 0, NewError("invalid_edit", fmt.Sprintf("edits[%d].oldText must not be empty", index))
		}
		if strings.Count(normalized, oldText) != 1 {
			return nil, 0, NewError("edit_match_failed", fmt.Sprintf("edits[%d].oldText must match exactly once in the original file", index))
		}
		start := strings.Index(normalized, oldText)
		line := 1 + strings.Count(normalized[:start], "\n")
		if firstLine == 0 || line < firstLine {
			firstLine = line
		}
		located = append(located, locatedReplacement{start: start, end: start + len(oldText), newText: newText})
	}
	sort.Slice(located, func(i, j int) bool { return located[i].start < located[j].start })
	for index := 1; index < len(located); index++ {
		if located[index].start < located[index-1].end {
			return nil, 0, NewError("overlapping_edits", "edits must not overlap or nest")
		}
	}
	var output strings.Builder
	position := 0
	for _, edit := range located {
		output.WriteString(normalized[position:edit.start])
		output.WriteString(edit.newText)
		position = edit.end
	}
	output.WriteString(normalized[position:])
	final := restoreLineEndings(output.String(), lineEnding)
	return []byte(bom + final), firstLine, nil
}

func splitUTF8BOM(content string) (string, string) {
	bom := string([]byte{0xef, 0xbb, 0xbf})
	if strings.HasPrefix(content, bom) {
		return bom, strings.TrimPrefix(content, bom)
	}
	return "", content
}

func detectLineEnding(content string) string {
	if bytes.Count([]byte(content), []byte("\r\n")) > 0 {
		return "\r\n"
	}
	return "\n"
}

func normalizeLineEndings(content string) string {
	return strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
}

func restoreLineEndings(content, lineEnding string) string {
	if lineEnding == "\r\n" {
		return strings.ReplaceAll(content, "\n", "\r\n")
	}
	return content
}
