package runtimebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

type Paths struct {
	Root         string
	Bash         string
	BusyBox      string
	ShellProfile string
}

type Resolver struct {
	baseDir      string
	explicitBash string
	explicitBusy string
	once         sync.Once
	paths        Paths
	err          error
}

func NewResolver(baseDir, explicitBash, explicitBusy string) *Resolver {
	return &Resolver{baseDir: baseDir, explicitBash: explicitBash, explicitBusy: explicitBusy}
}

func (r *Resolver) Resolve() (Paths, error) {
	r.once.Do(func() {
		r.paths, r.err = r.resolve()
	})
	return r.paths, r.err
}

func (r *Resolver) resolve() (Paths, error) {
	if runtime.GOOS == "windows" && runtime.GOARCH != "amd64" {
		return Paths{}, fmt.Errorf("Windows %s is not supported; only windows/amd64 is supported", runtime.GOARCH)
	}
	if r.explicitBash != "" || r.explicitBusy != "" {
		return resolveExplicit(r.explicitBash, r.explicitBusy)
	}
	if runtime.GOOS != "windows" {
		bash, err := findSystemBash()
		if err != nil {
			return Paths{}, err
		}
		return Paths{Bash: bash, BusyBox: r.explicitBusy, ShellProfile: "system-bash"}, nil
	}
	return installEmbedded(r.baseDir)
}

func resolveExplicit(bashPath, busyPath string) (Paths, error) {
	var err error
	if bashPath != "" {
		bashPath, err = filepath.Abs(bashPath)
		if err != nil {
			return Paths{}, fmt.Errorf("resolve bash path: %w", err)
		}
		if err := requireFile(bashPath); err != nil {
			return Paths{}, err
		}
	}
	if busyPath != "" {
		busyPath, err = filepath.Abs(busyPath)
		if err != nil {
			return Paths{}, fmt.Errorf("resolve busybox path: %w", err)
		}
		if err := requireFile(busyPath); err != nil {
			return Paths{}, err
		}
	}
	if bashPath == "" && busyPath == "" {
		return Paths{}, errors.New("a Bash or BusyBox executable is required")
	}
	shellProfile := "explicit-bash"
	runtimeRoot := ""
	if bashPath != "" {
		runtimeRoot = runtimeRootFromBash(bashPath)
	}
	if busyPath != "" {
		shellProfile = "busybox-sh"
	}
	return Paths{Root: runtimeRoot, Bash: bashPath, BusyBox: busyPath, ShellProfile: shellProfile}, nil
}

func runtimeRootFromBash(bashPath string) string {
	binDir := filepath.Dir(bashPath)
	usrDir := filepath.Dir(binDir)
	if strings.EqualFold(filepath.Base(binDir), "bin") && strings.EqualFold(filepath.Base(usrDir), "usr") {
		return filepath.Dir(usrDir)
	}
	return binDir
}

func findSystemBash() (string, error) {
	for _, candidate := range []string{"/bin/bash", "/usr/bin/bash"} {
		if err := requireFile(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", errors.New("bash was not found; use --bash-path")
}

func installEmbedded(baseDir string) (Paths, error) {
	archDir := "assets/windows_" + runtime.GOARCH
	files, err := collectFiles(archDir)
	if err != nil {
		return Paths{}, err
	}
	if len(files) == 0 {
		return Paths{}, fmt.Errorf("no embedded runtime for windows/%s; add files under internal/runtimebundle/%s", runtime.GOARCH, archDir)
	}
	hash, err := bundleHash(files)
	if err != nil {
		return Paths{}, err
	}
	if baseDir == "" {
		baseDir, err = defaultBaseDir()
		if err != nil {
			return Paths{}, err
		}
	}
	target := filepath.Join(baseDir, hash[:16])
	if err := ensureInstalled(target, archDir, files); err != nil {
		return Paths{}, err
	}
	if err := ensureRuntimeDirectories(target); err != nil {
		return Paths{}, err
	}
	busyPath := filepath.Join(target, "bin", "busybox.exe")
	if err := requireFile(busyPath); err != nil {
		return Paths{}, fmt.Errorf("embedded runtime is incomplete: %w", err)
	}
	return Paths{Root: target, BusyBox: busyPath, ShellProfile: "busybox-sh"}, nil
}

func ensureRuntimeDirectories(target string) error {
	tmpDir := filepath.Join(target, "tmp")
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return fmt.Errorf("create runtime temporary directory: %w", err)
	}
	return nil
}

func collectFiles(root string) ([]string, error) {
	var files []string
	err := fs.WalkDir(assets, root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || ignoredAsset(path.Base(name)) {
			return nil
		}
		files = append(files, name)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan embedded runtime: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func ignoredAsset(name string) bool {
	return name == "_placeholder" || name == ".DS_Store"
}

func bundleHash(files []string) (string, error) {
	h := sha256.New()
	for _, name := range files {
		data, err := assets.ReadFile(name)
		if err != nil {
			return "", fmt.Errorf("read embedded file %s: %w", name, err)
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func defaultBaseDir() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	return filepath.Join(cacheDir, "AgentRemote", "runtime"), nil
}

func ensureInstalled(target, sourceRoot string, files []string) error {
	complete := filepath.Join(target, ".complete")
	if _, err := os.Stat(complete); err == nil {
		return verifyInstalled(target, sourceRoot, files)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return fmt.Errorf("create runtime base directory: %w", err)
	}
	temp, err := os.MkdirTemp(filepath.Dir(target), ".install-")
	if err != nil {
		return fmt.Errorf("create runtime staging directory: %w", err)
	}
	defer os.RemoveAll(temp)
	if err := extractFiles(temp, sourceRoot, files); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(temp, ".complete"), []byte("ok\n"), 0600); err != nil {
		return fmt.Errorf("write runtime marker: %w", err)
	}
	if err := os.Rename(temp, target); err != nil {
		if _, statErr := os.Stat(complete); statErr == nil {
			return verifyInstalled(target, sourceRoot, files)
		}
		return fmt.Errorf("activate runtime directory: %w", err)
	}
	return verifyInstalled(target, sourceRoot, files)
}

func extractFiles(target, sourceRoot string, files []string) error {
	for _, name := range files {
		relative := strings.TrimPrefix(name, sourceRoot+"/")
		if relative == name || !safeRelativePath(relative) {
			return fmt.Errorf("unsafe embedded runtime path %q", name)
		}
		data, err := assets.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read embedded runtime file %s: %w", name, err)
		}
		destination := filepath.Join(target, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return fmt.Errorf("create runtime directory for %s: %w", relative, err)
		}
		if err := writeExclusive(destination, data); err != nil {
			return err
		}
	}
	return nil
}

func writeExclusive(destination string, data []byte) error {
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return fmt.Errorf("create runtime file %s: %w", destination, err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write runtime file %s: %w", destination, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close runtime file %s: %w", destination, err)
	}
	return nil
}

func verifyInstalled(target, sourceRoot string, files []string) error {
	for _, name := range files {
		relative := strings.TrimPrefix(name, sourceRoot+"/")
		expected, err := assets.ReadFile(name)
		if err != nil {
			return err
		}
		actual, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(relative)))
		if err != nil {
			return fmt.Errorf("verify runtime file %s: %w", relative, err)
		}
		if sha256.Sum256(actual) != sha256.Sum256(expected) {
			return fmt.Errorf("runtime file checksum mismatch: %s", relative)
		}
	}
	return nil
}

func safeRelativePath(name string) bool {
	cleaned := path.Clean(name)
	return cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../") && !path.IsAbs(cleaned)
}

func requireFile(name string) error {
	info, err := os.Stat(name)
	if err != nil {
		return fmt.Errorf("required file %s: %w", name, err)
	}
	if info.IsDir() {
		return fmt.Errorf("required file %s is a directory", name)
	}
	return nil
}
