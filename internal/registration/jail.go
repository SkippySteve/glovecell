package registration

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
)

// Catalog roots exposed through the mount. Nothing outside these paths is
// readable through the helper or FUSE node.
var DefaultRoots = []string{"/workspace/repo", "/workspace/worktrees"}

// Confine returns a cleaned absolute path under one of the allowed roots.
// It rejects empty paths, relative segments that escape, and any cleaned
// result that is not the root itself or a descendant.
func Confine(raw string, roots []string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("path required")
	}
	if !path.IsAbs(raw) {
		return "", errors.New("path must be absolute")
	}
	cleaned := path.Clean(raw)
	if cleaned != raw && strings.Contains(raw, "..") {
		// Clean already collapsed ..; still require the result stay jailed.
	}
	if cleaned == "/" || cleaned == "." {
		return "", errors.New("path escapes jail")
	}
	for _, root := range roots {
		root = path.Clean(root)
		if cleaned == root || strings.HasPrefix(cleaned, root+"/") {
			return cleaned, nil
		}
	}
	return "", errors.New("path escapes jail")
}

// JoinUnder joins name under parent after confining both.
func JoinUnder(parent, name string, roots []string) (string, error) {
	base, err := Confine(parent, roots)
	if err != nil {
		return "", err
	}
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return "", errors.New("invalid name")
	}
	return Confine(path.Join(base, name), roots)
}

// RealpathUnder resolves a path with the provided realpath function and
// requires the result to stay under a root. Use for open/read after Lstat.
func RealpathUnder(raw string, roots []string, realpath func(string) (string, error)) (string, error) {
	confined, err := Confine(raw, roots)
	if err != nil {
		return "", err
	}
	if realpath == nil {
		return confined, nil
	}
	resolved, err := realpath(confined)
	if err != nil {
		return "", err
	}
	resolved = filepath.ToSlash(path.Clean(resolved))
	if !path.IsAbs(resolved) {
		return "", errors.New("resolved path must be absolute")
	}
	return Confine(resolved, roots)
}
