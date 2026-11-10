package skills

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Store struct{ root string }

func NewStore(root string) *Store { return &Store{root: root} }

type snapshot struct {
	Version int          `json:"version"`
	Files   []storedFile `json:"files"`
}

type storedFile struct {
	Path string `json:"path"`
	Hash string `json:"hash,omitempty"`
	Mode uint32 `json:"mode"`
	Link string `json:"link,omitempty"`
}

// Save publishes immutable files before publishing their snapshot manifest.
func (s *Store) Save(tree Tree) (string, error) {
	if err := validateTree(tree); err != nil {
		return "", err
	}
	for _, directory := range []string{s.root, filepath.Join(s.root, "objects"), filepath.Join(s.root, "snapshots")} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return "", err
		}
		info, err := os.Lstat(directory)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("store directory is not a real directory: %s", directory)
		}
	}
	manifest := snapshot{Version: 1, Files: []storedFile{}}
	for _, name := range sortedNames(tree) {
		file := tree[name]
		entry := storedFile{Path: name, Mode: file.Mode, Link: file.Link}
		if file.Link == "" {
			entry.Hash = digest(file.Data)
			if err := immutableWrite(filepath.Join(s.root, "objects", entry.Hash), file.Data); err != nil {
				return "", err
			}
		}
		manifest.Files = append(manifest.Files, entry)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if len(data) > 8<<20 {
		return "", errors.New("snapshot manifest exceeds 8 MiB")
	}
	id := digest(data)
	if err := immutableWrite(filepath.Join(s.root, "snapshots", id+".json"), data); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) Load(id string) (Tree, error) {
	if !validDigest(id) {
		return nil, fmt.Errorf("invalid snapshot ID")
	}
	for _, directory := range []string{s.root, filepath.Join(s.root, "objects"), filepath.Join(s.root, "snapshots")} {
		info, err := os.Lstat(directory)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("store directory is not a real directory: %s", directory)
		}
	}
	data, err := regularRead(filepath.Join(s.root, "snapshots", id+".json"), 8<<20)
	if err != nil {
		return nil, err
	}
	if digest(data) != id {
		return nil, fmt.Errorf("snapshot manifest checksum mismatch: %s", id)
	}
	var manifest snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("snapshot manifest contains trailing data")
	}
	if manifest.Version != 1 {
		return nil, fmt.Errorf("unsupported snapshot version %d", manifest.Version)
	}
	if len(manifest.Files) > MaxTreeFiles {
		return nil, fmt.Errorf("snapshot file count exceeds limit")
	}
	tree := Tree{}
	var total int64
	for _, entry := range manifest.Files {
		if err := safeName(entry.Path); err != nil {
			return nil, err
		}
		if _, exists := tree[entry.Path]; exists {
			return nil, fmt.Errorf("duplicate snapshot path %s", entry.Path)
		}
		file := File{Mode: entry.Mode, Link: entry.Link}
		if entry.Link == "" {
			if !validDigest(entry.Hash) {
				return nil, fmt.Errorf("invalid blob ID at %s", entry.Path)
			}
			file.Data, err = regularRead(filepath.Join(s.root, "objects", entry.Hash), MaxFileBytes)
			if err != nil {
				return nil, fmt.Errorf("load %s: %w", entry.Path, err)
			}
			if digest(file.Data) != entry.Hash {
				return nil, fmt.Errorf("blob checksum mismatch at %s", entry.Path)
			}
			total += int64(len(file.Data))
			if total > MaxTreeBytes {
				return nil, fmt.Errorf("snapshot byte count exceeds limit")
			}
		} else if entry.Hash != "" {
			return nil, fmt.Errorf("link %s has blob data", entry.Path)
		}
		tree[entry.Path] = file
	}
	if err := validateTree(tree); err != nil {
		return nil, err
	}
	return tree, nil
}

func validDigest(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	if err != nil {
		return false
	}
	for _, c := range id {
		if c >= 'A' && c <= 'F' {
			return false
		}
	}
	return true
}

func regularRead(filename string, limit int64) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("store object is not a regular file: %s", filename)
	}
	return readBounded(filename, limit)
}

func immutableWrite(filename string, data []byte) error {
	check := func() error {
		existing, err := regularRead(filename, int64(len(data)))
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("immutable store content differs: %s", filename)
		}
		return nil
	}
	if _, err := os.Lstat(filename); err == nil {
		return check()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(filename), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// Linking provides atomic publication without replacing an existing object.
	if err = os.Link(tmp.Name(), filename); errors.Is(err, os.ErrExist) {
		return check()
	}
	return err
}
