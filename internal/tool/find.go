package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type findInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type FindEntry struct {
	Path        string `json:"path"`
	IsDirectory bool   `json:"is_directory"`
}

func (s *Service) executeFind(input json.RawMessage) ([]FindEntry, error) {
	var request findInput
	if err := decodeInput(input, &request); err != nil {
		return nil, err
	}
	if request.MaxResults <= 0 || request.MaxResults > 100 {
		request.MaxResults = 20
	}
	entries, err := s.findEntries(request.Query)
	if err != nil {
		return nil, err
	}
	if len(entries) > request.MaxResults {
		entries = entries[:request.MaxResults]
	}
	return entries, nil
}

func (s *Service) findEntries(query string) ([]FindEntry, error) {
	query = filepath.ToSlash(strings.TrimSpace(query))
	root := s.paths.Root()
	searchRoot, nameQuery := findSearchScope(root, query)
	if !pathWithinRoot(root, searchRoot) {
		return nil, NewError("path_outside_workspace", "search path is outside the configured workspace root")
	}
	if _, err := os.Stat(searchRoot); err != nil {
		return nil, NewError("path_not_found", "search path does not exist")
	}

	var entries []FindEntry
	err := filepath.WalkDir(searchRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != searchRoot && entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if path == searchRoot {
			if nameQuery != "" && !entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		displayPath := filepath.ToSlash(relative)
		if nameQuery != "" && !matchesFindQuery(displayPath, nameQuery) {
			return nil
		}
		entries = append(entries, FindEntry{Path: displayPath, IsDirectory: entry.IsDir()})
		return nil
	})
	if err != nil {
		return nil, WrapError("find_failed", "search files", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i], entries[j]
		leftScore := findScore(left.Path, nameQuery, left.IsDirectory)
		rightScore := findScore(right.Path, nameQuery, right.IsDirectory)
		if leftScore != rightScore {
			return leftScore > rightScore
		}
		leftDepth := strings.Count(left.Path, "/")
		rightDepth := strings.Count(right.Path, "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return left.Path < right.Path
	})
	return entries, nil
}

func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func findSearchScope(root, query string) (string, string) {
	if query == "" || strings.HasSuffix(query, "/") {
		if query == "" {
			return root, ""
		}
		return filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(query, "/"))), ""
	}
	directory, name := filepath.Split(query)
	return filepath.Join(root, filepath.FromSlash(directory)), strings.TrimSuffix(name, "/")
}

func matchesFindQuery(path, query string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	query = strings.ToLower(filepath.ToSlash(query))
	name := strings.ToLower(filepath.Base(path))
	return name == query || strings.HasPrefix(name, query) || strings.Contains(name, query) || strings.Contains(path, query)
}

func findScore(path, query string, isDirectory bool) int {
	if query == "" {
		return 1
	}
	name := strings.ToLower(filepath.Base(path))
	query = strings.ToLower(filepath.ToSlash(query))
	score := 10
	switch {
	case name == query:
		score = 100
	case strings.HasPrefix(name, query):
		score = 80
	case strings.Contains(name, query):
		score = 50
	case strings.Contains(strings.ToLower(filepath.ToSlash(path)), query):
		score = 30
	default:
		return 0
	}
	if isDirectory {
		score++
	}
	return score
}
