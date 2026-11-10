package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	MaxFileBytes = 16 << 20
	MaxTreeBytes = 128 << 20
	MaxTreeFiles = 10000
	maxDepth     = 64
)

// Tree contains files relative to one skill directory. Directory entries are implicit.
type Tree map[string]File

type File struct {
	Data []byte `json:"data,omitempty"`
	Mode uint32 `json:"mode"`
	Link string `json:"link,omitempty"`
}

// ReadTree reads a skill without following nested links out of its directory.
func ReadTree(directory string) (Tree, error) {
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve skill: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill is not a directory: %s", directory)
	}
	scoped, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer scoped.Close()
	tree := Tree{}
	var total int64
	err = filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filename == root {
			return nil
		}
		rel, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.Count(rel, "/") > maxDepth {
			return fmt.Errorf("skill nesting exceeds %d directories", maxDepth)
		}
		// Git history is not part of a skill's distributed contents.
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		if len(tree) >= MaxTreeFiles {
			return fmt.Errorf("skill exceeds %d files", MaxTreeFiles)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		file := File{Mode: uint32(info.Mode().Perm())}
		if info.Mode()&os.ModeSymlink != 0 {
			file.Link, err = os.Readlink(filename)
			if err != nil {
				return err
			}
			if err = safeLink(rel, file.Link); err != nil {
				return err
			}
			target, err := filepath.EvalSymlinks(filename)
			if err != nil {
				return fmt.Errorf("resolve link %s: %w", rel, err)
			}
			if !within(root, target) {
				return fmt.Errorf("link %s leaves skill directory", rel)
			}
		} else {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported file type: %s", rel)
			}
			opened, openErr := scoped.Open(filepath.FromSlash(rel))
			if openErr != nil {
				return fmt.Errorf("open %s: %w", rel, openErr)
			}
			file.Data, err = readFileBounded(opened, MaxFileBytes)
			opened.Close()
			if err != nil {
				return fmt.Errorf("read %s: %w", rel, err)
			}
			total += int64(len(file.Data))
			if total > MaxTreeBytes {
				return fmt.Errorf("skill exceeds %d bytes", MaxTreeBytes)
			}
		}
		tree[rel] = file
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := validateTree(tree); err != nil {
		return nil, err
	}
	return tree, nil
}

func readBounded(filename string, limit int64) ([]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readFileBounded(f, limit)
}

func readFileBounded(f *os.File, limit int64) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return b, err
}

func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func safeName(name string) error {
	if name == "." || name == "" || !fs.ValidPath(name) || strings.ContainsAny(name, "\\\x00") {
		return fmt.Errorf("unsafe snapshot path %q", name)
	}
	return nil
}

func safeLink(name, target string) error {
	if target == "" || strings.ContainsAny(target, "\\\x00") || path.IsAbs(target) {
		return fmt.Errorf("unsafe link target at %s", name)
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("link %s leaves skill directory", name)
	}
	return nil
}

func validateTree(tree Tree) error {
	if len(tree) > MaxTreeFiles {
		return fmt.Errorf("snapshot exceeds %d files", MaxTreeFiles)
	}
	var size int64
	for name, file := range tree {
		if err := safeName(name); err != nil {
			return err
		}
		if strings.Count(name, "/") > maxDepth {
			return fmt.Errorf("snapshot nesting exceeds limit")
		}
		if file.Mode > 0777 {
			return fmt.Errorf("invalid permission bits for %s", name)
		}
		if file.Link != "" {
			if len(file.Data) != 0 {
				return fmt.Errorf("link %s also contains data", name)
			}
			if err := safeLink(name, file.Link); err != nil {
				return err
			}
		}
		if len(file.Data) > MaxFileBytes {
			return fmt.Errorf("snapshot file %s is too large", name)
		}
		size += int64(len(file.Data))
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, ok := tree[parent]; ok {
				return fmt.Errorf("snapshot path %s is beneath another file", name)
			}
		}
	}
	if size > MaxTreeBytes {
		return fmt.Errorf("snapshot exceeds %d bytes", MaxTreeBytes)
	}
	return nil
}

func digest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func sortedNames(tree Tree) []string {
	names := make([]string, 0, len(tree))
	for name := range tree {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
