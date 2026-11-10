// Package integrations uses external installers only in isolated staging
// directories. Publishing accepted contents to agent paths is a separate step.
package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jdevera/dotagents/internal/skills"
)

type Fetched struct {
	Files  skills.Tree `json:"files"`
	Source string      `json:"source"`
	Ref    string      `json:"ref"`
	Commit string      `json:"commit"`
	Name   string      `json:"name"`
	// Metadata is Skillshare's provenance entry, excluding local timestamps.
	Metadata map[string]any `json:"metadata"`
}

var skillName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var commitRef = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
var githubSource = regexp.MustCompile(`^(?:https://github\.com/|github\.com/|git@github\.com:)?[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*$`)

// Fetch asks Skillshare to install one pinned GitHub skill into a temporary
// library with no agent targets. It never invokes init, sync, force, or bypasses
// auditing. HOME and all live agent configuration remain unchanged.
func Fetch(ctx context.Context, source, name, ref string) (Fetched, error) {
	if !skillName.MatchString(name) || name == "." || name == ".." {
		return Fetched{}, errors.New("skill name must contain only letters, digits, dots, underscores, and hyphens")
	}
	if !commitRef.MatchString(ref) {
		return Fetched{}, errors.New("fetch requires a full 40-character Git commit SHA; branches and tags are not reproducible pins")
	}
	if !githubSource.MatchString(source) {
		return Fetched{}, errors.New("source must be a GitHub owner/repository or GitHub URL without credentials, query, or fragment")
	}
	for _, part := range strings.Split(strings.TrimPrefix(strings.TrimPrefix(source, "https://"), "git@github.com:"), "/") {
		if part == "." || part == ".." {
			return Fetched{}, errors.New("source cannot contain dot path segments")
		}
	}
	if err := ctx.Err(); err != nil {
		return Fetched{}, err
	}
	executable, err := exec.LookPath("skillshare")
	if err != nil {
		return Fetched{}, errors.New("skillshare is not installed or not on PATH")
	}
	stage, err := os.MkdirTemp("", "dotagents-fetch-")
	if err != nil {
		return Fetched{}, err
	}
	defer os.RemoveAll(stage)
	library := filepath.Join(stage, "library")
	for _, path := range []string{library, filepath.Join(stage, "agents"), filepath.Join(stage, "config"), filepath.Join(stage, "data"), filepath.Join(stage, "state"), filepath.Join(stage, "cache")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return Fetched{}, err
		}
	}
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	config := "sources:\n  skills: " + quote(library) + "\n  agents: " + quote(filepath.Join(stage, "agents")) + "\nmode: merge\ntargets: {}\n"
	configPath := filepath.Join(stage, "config/config.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		return Fetched{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "install", source, "--skill", name, "--branch", ref, "--kind", "skill", "-g")
	command.Dir = stage
	command.Env = isolatedEnvironment(stage, configPath)
	var output boundedOutput
	command.Stdout = &output
	command.Stderr = &output
	command.WaitDelay = 3 * time.Second
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return Fetched{}, fmt.Errorf("Skillshare fetch stopped: %w", ctx.Err())
		}
		return Fetched{}, fmt.Errorf("Skillshare rejected or failed the staged install: %w\n%s", err, output.String())
	}
	return inspectFetch(library, source, name, strings.ToLower(ref))
}

func isolatedEnvironment(stage, configPath string) []string {
	replacements := map[string]string{
		"SKILLSHARE_CONFIG": configPath,
		"XDG_CONFIG_HOME":   filepath.Join(stage, "config"),
		"XDG_DATA_HOME":     filepath.Join(stage, "data"),
		"XDG_STATE_HOME":    filepath.Join(stage, "state"),
		"XDG_CACHE_HOME":    filepath.Join(stage, "cache"),
	}
	env := make([]string, 0, len(os.Environ())+len(replacements))
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if _, replace := replacements[name]; !replace && !strings.HasPrefix(name, "SKILLSHARE_") {
			env = append(env, value)
		}
	}
	for name, value := range replacements {
		env = append(env, name+"="+value)
	}
	return env
}

func inspectFetch(library, source, name, ref string) (Fetched, error) {
	metadata, err := os.ReadFile(filepath.Join(library, ".metadata.json"))
	if err != nil {
		return Fetched{}, fmt.Errorf("Skillshare did not produce provenance metadata: %w", err)
	}
	if len(metadata) > 4<<20 {
		return Fetched{}, errors.New("Skillshare provenance metadata exceeds 4 MiB")
	}
	var document struct {
		Version int                       `json:"version"`
		Entries map[string]map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(metadata, &document); err != nil || document.Version != 1 {
		return Fetched{}, errors.New("unsupported Skillshare provenance metadata")
	}
	entry, found := document.Entries[name]
	if !found || len(document.Entries) != 1 {
		return Fetched{}, errors.New("Skillshare did not install exactly the requested skill")
	}
	commit, _ := entry["commit"].(string)
	if strings.ToLower(commit) != ref {
		return Fetched{}, fmt.Errorf("Skillshare resolved commit %q instead of requested %s", commit, ref)
	}
	path := filepath.Join(library, name)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Fetched{}, err
	}
	realLibrary, err := filepath.EvalSymlinks(library)
	if err != nil {
		return Fetched{}, err
	}
	relative, err := filepath.Rel(realLibrary, real)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return Fetched{}, errors.New("installed skill escaped the staging library")
	}
	files, err := skills.ReadTree(path)
	if err != nil {
		return Fetched{}, fmt.Errorf("read staged skill: %w", err)
	}
	if file, ok := files["SKILL.md"]; !ok || len(file.Data) == 0 || file.Link != "" {
		return Fetched{}, errors.New("staged skill has no regular, nonempty SKILL.md")
	}
	hashes, ok := entry["file_hashes"].(map[string]any)
	if !ok || len(hashes) != len(files) {
		return Fetched{}, errors.New("staged skill file inventory does not match provenance hashes")
	}
	for path, file := range files {
		if file.Link != "" {
			return Fetched{}, fmt.Errorf("staged symlink %s needs explicit review before adoption", path)
		}
		hash := sha256.Sum256(file.Data)
		expected, ok := hashes[path].(string)
		if !ok || expected != "sha256:"+hex.EncodeToString(hash[:]) {
			return Fetched{}, fmt.Errorf("staged skill file %s does not match its provenance hash", path)
		}
	}
	delete(entry, "installed_at")
	return Fetched{Files: files, Source: source, Ref: ref, Commit: commit, Name: name, Metadata: entry}, nil
}

type boundedOutput struct {
	mu        sync.Mutex
	data      bytes.Buffer
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := (64 << 10) - b.data.Len()
	if len(p) > remaining {
		b.data.Write(p[:remaining])
		b.truncated = true
	} else {
		b.data.Write(p)
	}
	return len(p), nil
}

func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.truncated {
		return b.data.String() + "\n[installer output truncated]"
	}
	return b.data.String()
}
