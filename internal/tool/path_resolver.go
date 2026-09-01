package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type PathResolver struct {
	root         string
	resolvedRoot string
	allowOutside bool
}

func NewPathResolver(root string, allowOutside bool) (*PathResolver, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root links: %w", err)
	}
	return &PathResolver{root: absoluteRoot, resolvedRoot: resolvedRoot, allowOutside: allowOutside}, nil
}

func (r *PathResolver) Root() string {
	return r.root
}

func (r *PathResolver) Resolve(name string, forWrite bool) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", NewError("invalid_path", "path must not be empty")
	}
	candidate := name
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(r.root, candidate)
	}
	absolute, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", WrapError("invalid_path", "resolve path", err)
	}
	if r.allowOutside {
		return absolute, nil
	}
	if !isWithin(r.root, absolute) {
		return "", NewError("path_outside_workspace", "path is outside the configured workspace root")
	}
	resolved, err := resolveExistingPath(absolute, forWrite)
	if err != nil {
		return "", WrapError("invalid_path", "resolve path links", err)
	}
	if !isWithin(r.resolvedRoot, resolved) {
		return "", NewError("path_outside_workspace", "path resolves outside the configured workspace root")
	}
	return absolute, nil
}

func resolveExistingPath(name string, forWrite bool) (string, error) {
	resolved, err := filepath.EvalSymlinks(name)
	if err == nil {
		return resolved, nil
	}
	if !forWrite || !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(name)
	for {
		resolvedParent, parentErr := filepath.EvalSymlinks(parent)
		if parentErr == nil {
			return filepath.Join(resolvedParent, strings.TrimPrefix(name, parent+string(filepath.Separator))), nil
		}
		if !os.IsNotExist(parentErr) {
			return "", parentErr
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", err
		}
		parent = next
	}
}

func isWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
