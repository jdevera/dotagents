package app

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BackupReceipt struct {
	Archive string   `json:"archive"`
	SHA256  string   `json:"sha256"`
	Files   int      `json:"files"`
	Bytes   int64    `json:"bytes"`
	Sources []string `json:"sources"`
}

// Backup takes a local, permission-restricted snapshot before a migration.
// Unlike a catalog, it includes credentials and never belongs in Git.
func (a *App) Backup() (BackupReceipt, error) {
	base := filepath.Join(a.Repo, ".dotagents", "backups")
	if err := os.MkdirAll(base, 0700); err != nil {
		return BackupReceipt{}, err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	path := filepath.Join(base, stamp+".tar.gz")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return BackupReceipt{}, err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	r := BackupReceipt{Archive: path, Sources: []string{}}
	hashes := map[string]string{}
	add := func(source, label string, repo bool) error {
		if _, err := os.Lstat(source); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		r.Sources = append(r.Sources, source)
		return filepath.WalkDir(source, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == base || strings.HasPrefix(path, base+string(filepath.Separator)) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			if repo && rel == "dist" {
				return filepath.SkipDir
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("unsupported backup entry %s", path)
			}
			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				link, err = os.Readlink(path)
				if err != nil {
					return err
				}
			}
			header, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			header.Name = filepath.ToSlash(filepath.Join(label, rel))
			header.Uid = 0
			header.Gid = 0
			header.Uname = ""
			header.Gname = ""
			if err = tw.WriteHeader(header); err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				input, err := os.Open(path)
				if err != nil {
					return err
				}
				hash := sha256.New()
				n, e := io.Copy(io.MultiWriter(tw, hash), input)
				input.Close()
				if e != nil {
					return e
				}
				if n != info.Size() {
					return fmt.Errorf("file changed during backup: %s", path)
				}
				hashes[header.Name] = hex.EncodeToString(hash.Sum(nil))
				r.Files++
				r.Bytes += n
			}
			return nil
		})
	}
	for _, relative := range []string{".agents", ".codex/skills", ".codex/config.toml", ".codex/AGENTS.md", ".claude/skills", ".claude/settings.json", ".claude.json", ".claude/CLAUDE.md", ".config/skillshare", ".local/share/skillshare", ".local/state/skillshare"} {
		source := filepath.Join(a.Home, relative)
		// The repo is captured separately and its backup output must never recurse.
		if filepath.Clean(source) == filepath.Clean(a.Repo) {
			continue
		}
		if err := add(source, filepath.Join("home", relative), false); err != nil {
			return BackupReceipt{}, err
		}
	}
	if err := add(a.Repo, "repository", true); err != nil {
		return BackupReceipt{}, err
	}
	if err = tw.Close(); err != nil {
		return BackupReceipt{}, err
	}
	if err = gz.Close(); err != nil {
		return BackupReceipt{}, err
	}
	if err = f.Sync(); err != nil {
		return BackupReceipt{}, err
	}
	if err = f.Close(); err != nil {
		return BackupReceipt{}, err
	}
	// Read the completed archive back, checking every regular file against its
	// source digest. A successful tar close alone isn't a verified backup.
	input, err := os.Open(path)
	if err != nil {
		return BackupReceipt{}, err
	}
	defer input.Close()
	archiveHash := sha256.New()
	reader, err := gzip.NewReader(io.TeeReader(input, archiveHash))
	if err != nil {
		return BackupReceipt{}, err
	}
	defer reader.Close()
	tr := tar.NewReader(reader)
	seen := 0
	for {
		header, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return BackupReceipt{}, e
		}
		if header.Typeflag == tar.TypeReg {
			hash := sha256.New()
			if _, e = io.Copy(hash, tr); e != nil {
				return BackupReceipt{}, e
			}
			if hashes[header.Name] != hex.EncodeToString(hash.Sum(nil)) {
				return BackupReceipt{}, fmt.Errorf("backup verification failed: %s", header.Name)
			}
			seen++
		}
	}
	if _, err = io.Copy(io.Discard, reader); err != nil {
		return BackupReceipt{}, err
	}
	if _, err = io.Copy(io.Discard, io.TeeReader(input, archiveHash)); err != nil {
		return BackupReceipt{}, err
	}
	if seen != r.Files {
		return BackupReceipt{}, fmt.Errorf("backup file count mismatch")
	}
	r.SHA256 = hex.EncodeToString(archiveHash.Sum(nil))
	data, _ := json.MarshalIndent(r, "", "  ")
	if err = atomicWrite(strings.TrimSuffix(path, ".tar.gz")+".json", append(data, '\n'), 0600); err != nil {
		return BackupReceipt{}, err
	}
	ok = true
	return r, nil
}
