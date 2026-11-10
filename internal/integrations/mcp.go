package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type MCPDesired struct {
	Client     string         `json:"client"`
	Name       string         `json:"name"`
	Definition map[string]any `json:"definition,omitempty"`
	Remove     bool           `json:"remove,omitempty"`
}

// MCPStage contains private native configuration bytes. Never serialize this
// value into reports or serve it over the dashboard API. Files and Before are
// keyed by paths relative to the supplied home. A missing Before value means
// the native file did not exist. Only changed files are included.
type MCPStage struct {
	Files  map[string][]byte
	Before map[string][]byte
}

// StageMCPProject uses the same native publishers but stages Claude's .mcp.json
// as an isolated account file. Returned keys refer only to project-local paths;
// no global settings are read, and no project trust settings are changed.
func StageMCPProject(ctx context.Context, project string, desired []MCPDesired) (MCPStage, error) {
	if err := ctx.Err(); err != nil {
		return MCPStage{}, err
	}
	if len(desired) == 0 {
		return MCPStage{Files: map[string][]byte{}, Before: map[string][]byte{}}, nil
	}
	stage, err := os.MkdirTemp("", "dotagents-project-mcp-")
	if err != nil {
		return MCPStage{}, err
	}
	defer os.RemoveAll(stage)
	selected := map[string]bool{}
	for _, item := range desired {
		selected[item.Client] = true
	}
	paths := map[string]string{}
	if selected["claude"] {
		paths[".mcp.json"] = ".claude.json"
	}
	if selected["codex"] {
		paths[".codex/config.toml"] = ".codex/config.toml"
	}
	for source, destination := range paths {
		if parent := filepath.Dir(source); parent != "." {
			info, err := os.Lstat(filepath.Join(project, parent))
			if err != nil && !os.IsNotExist(err) {
				return MCPStage{}, errors.New("cannot inspect project MCP configuration directory")
			}
			if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
				return MCPStage{}, errors.New("project MCP configuration directory cannot be a symlink")
			}
		}
		info, err := os.Lstat(filepath.Join(project, source))
		if err != nil && !os.IsNotExist(err) {
			return MCPStage{}, errors.New("cannot inspect project MCP configuration")
		}
		if err == nil && !info.Mode().IsRegular() {
			return MCPStage{}, errors.New("project MCP configuration cannot be a symlink or non-regular file")
		}
		data, err := readNative(filepath.Join(project, source))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return MCPStage{}, errors.New("cannot read project MCP settings; contents withheld")
		}
		path := filepath.Join(stage, destination)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return MCPStage{}, err
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			return MCPStage{}, err
		}
	}
	staged, err := StageMCP(ctx, stage, desired)
	if err != nil {
		return MCPStage{}, err
	}
	remap := func(files map[string][]byte) map[string][]byte {
		result := make(map[string][]byte, len(files))
		for path, data := range files {
			if path == ".claude.json" {
				path = ".mcp.json"
			}
			result[path] = data
		}
		return result
	}
	return MCPStage{Files: remap(staged.Files), Before: remap(staged.Before)}, nil
}

// StageMCP uses Skillshare's native publishers against copies in private
// temporary account directories. No native file, server, or authentication flow
// is touched. The caller must back up and transactionally publish Files.
func StageMCP(ctx context.Context, home string, desired []MCPDesired) (MCPStage, error) {
	result := MCPStage{Files: map[string][]byte{}, Before: map[string][]byte{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(desired) == 0 {
		return result, nil
	}
	byClient := map[string][]MCPDesired{}
	seen := map[string]bool{}
	for _, item := range desired {
		if item.Client != "claude" && item.Client != "codex" {
			return result, fmt.Errorf("unsupported MCP client %q", item.Client)
		}
		if item.Name == "" || strings.ContainsAny(item.Name, "\x00\r\n") || strings.HasPrefix(item.Name, "-") {
			return result, errors.New("invalid MCP name")
		}
		key := item.Client + "/" + item.Name
		if seen[key] || (!item.Remove && item.Definition == nil) {
			return result, fmt.Errorf("duplicate or incomplete MCP request for %s", key)
		}
		seen[key] = true
		byClient[item.Client] = append(byClient[item.Client], item)
	}
	executable, err := exec.LookPath("skillshare")
	if err != nil {
		return result, errors.New("skillshare is not installed or not on PATH")
	}
	clients := make([]string, 0, len(byClient))
	for client := range byClient {
		clients = append(clients, client)
	}
	sort.Strings(clients)
	for _, client := range clients {
		relative, old, updated, err := stageMCPClient(ctx, executable, home, client, byClient[client])
		if err != nil {
			return MCPStage{}, err
		}
		if !bytes.Equal(old, updated) {
			result.Before[relative] = old
			result.Files[relative] = updated
		}
	}
	return result, nil
}

func stageMCPClient(ctx context.Context, executable, home, client string, desired []MCPDesired) (relative string, old, updated []byte, err error) {
	relative, filename, field := ".claude.json", ".claude.json", "mcpServers"
	if client == "codex" {
		relative, filename, field = ".codex/config.toml", "config.toml", "mcp_servers"
	}
	old, err = readNative(filepath.Join(home, relative))
	if err != nil && !os.IsNotExist(err) {
		return relative, nil, nil, fmt.Errorf("cannot read %s configuration", client)
	}
	if os.IsNotExist(err) {
		old = nil
	}
	original, err := decodeNative(client, old)
	if err != nil {
		return relative, nil, nil, err
	}
	servers, err := serverMap(original, field)
	if err != nil {
		return relative, nil, nil, err
	}
	allSame := true
	allowed := map[string]bool{}
	for _, item := range desired {
		allowed[item.Name] = true
		existing, exists := servers[item.Name]
		if item.Remove {
			allSame = allSame && !exists
		} else {
			allSame = allSame && equivalentDefinition(client, existing, item.Definition)
		}
	}
	if allSame {
		return relative, old, old, nil
	}
	stage, err := os.MkdirTemp("", "dotagents-mcp-")
	if err != nil {
		return relative, nil, nil, err
	}
	defer os.RemoveAll(stage)
	for _, name := range []string{"library", "agents", "config", "data", "state", "cache", "account"} {
		if err := os.MkdirAll(filepath.Join(stage, name), 0700); err != nil {
			return relative, nil, nil, err
		}
	}
	nativePath := filepath.Join(stage, "account", filename)
	if old != nil {
		if err := os.WriteFile(nativePath, old, 0600); err != nil {
			return relative, nil, nil, err
		}
	}
	alias := "staged-" + client
	configPath := filepath.Join(stage, "config/config.yaml")
	config := map[string]any{
		"sources": map[string]any{"skills": filepath.Join(stage, "library"), "agents": filepath.Join(stage, "agents")},
		"targets": map[string]any{alias: map[string]any{"agent": client, "config_dir": filepath.Join(stage, "account")}},
		"mcp":     map[string]any{"servers": map[string]any{}},
	}
	configBytes, _ := json.Marshal(config) // JSON is a strict subset of YAML.
	if err := os.WriteFile(configPath, configBytes, 0600); err != nil {
		return relative, nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Dir = stage
		cmd.Env = isolatedEnvironment(stage, configPath)
		cmd.WaitDelay = 3 * time.Second
		var output boundedOutput
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Native settings and importer errors may contain credentials.
			return nil, fmt.Errorf("Skillshare could not safely stage %s MCP settings; unsupported fields or literal credentials may require environment references (%s)", client, err)
		}
		return []byte(output.String()), nil
	}
	importDefinition := func(name string, definition any) error {
		document := map[string]any{field: map[string]any{name: definition}}
		var data []byte
		var err error
		extension := ".json"
		if client == "codex" {
			data, err = toml.Marshal(document)
			extension = ".toml"
		} else {
			data, err = json.Marshal(document)
		}
		if err != nil {
			return errors.New("cannot encode selected MCP definition; contents withheld")
		}
		path := filepath.Join(stage, "selected"+extension)
		if err := os.WriteFile(path, data, 0600); err != nil {
			return err
		}
		_, err = run("mcp", "import", name, "--file", path, "--from", alias, "--target", alias, "--replace", "-g", "--json")
		return err
	}
	sync := func() error {
		data, err := run("sync", "mcp", "-g", "--dry-run", "--json")
		if err != nil {
			return err
		}
		var plan struct {
			Blocked bool `json:"blocked"`
			Changes []struct {
				Target, Path, Name string
			} `json:"changes"`
		}
		if err := json.Unmarshal(data, &plan); err != nil || plan.Blocked {
			return errors.New("Skillshare staged MCP plan is invalid or blocked; no live settings changed")
		}
		for _, change := range plan.Changes {
			if change.Target != alias || filepath.Clean(change.Path) != filepath.Clean(nativePath) || !allowed[change.Name] {
				return errors.New("Skillshare proposed an unexpected MCP target or entry; staging stopped")
			}
		}
		_, err = run("sync", "mcp", "-g", "--json")
		return err
	}
	// Establish temporary ownership only for explicitly selected existing names.
	// This allows Skillshare to remove them later without claiming unknown items.
	hasExisting := false
	for _, item := range desired {
		if existing, exists := servers[item.Name]; exists {
			if err := importDefinition(item.Name, existing); err != nil {
				return relative, nil, nil, err
			}
			hasExisting = true
		}
	}
	if hasExisting {
		if err := sync(); err != nil {
			return relative, nil, nil, err
		}
	}
	for _, item := range desired {
		if item.Remove {
			if _, exists := servers[item.Name]; !exists {
				continue
			}
			if _, err := run("mcp", "remove", item.Name, "-g", "--json"); err != nil {
				return relative, nil, nil, err
			}
		} else if err := importDefinition(item.Name, item.Definition); err != nil {
			return relative, nil, nil, err
		}
	}
	if err := sync(); err != nil {
		return relative, nil, nil, err
	}
	updated, err = readNative(nativePath)
	if err != nil {
		return relative, nil, nil, errors.New("Skillshare did not produce the expected staged native file")
	}
	actual, err := decodeNative(client, updated)
	if err != nil {
		return relative, nil, nil, err
	}
	actualServers, err := serverMap(actual, field)
	if err != nil {
		return relative, nil, nil, err
	}
	for _, item := range desired {
		value, exists := actualServers[item.Name]
		if item.Remove {
			if exists {
				return relative, nil, nil, errors.New("Skillshare left a selected MCP entry behind")
			}
		} else if !exists || !equivalentDefinition(client, value, item.Definition) {
			return relative, nil, nil, fmt.Errorf("Skillshare could not preserve all requested fields for MCP %q; no live settings changed", item.Name)
		}
		delete(actualServers, item.Name)
		delete(servers, item.Name)
	}
	// Comparing the whole documents after removing selected names proves that
	// unrelated MCP entries and all non-MCP settings survived the publisher.
	original[field] = servers
	actual[field] = actualServers
	if !equivalentJSON(original, actual) {
		return relative, nil, nil, errors.New("Skillshare altered unrelated native settings; no live settings changed")
	}
	return relative, old, updated, nil
}

func readNative(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, errors.New("native settings are not a regular file below 16 MiB")
	}
	return os.ReadFile(path)
}

func decodeNative(client string, data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var document map[string]any
	var err error
	if client == "codex" {
		err = toml.Unmarshal(data, &document)
	} else {
		err = json.Unmarshal(data, &document)
	}
	if err != nil || document == nil {
		return nil, fmt.Errorf("invalid %s native settings; contents withheld", client)
	}
	return document, nil
}

func serverMap(document map[string]any, field string) (map[string]any, error) {
	if _, exists := document[field]; !exists {
		return map[string]any{}, nil
	}
	servers, ok := document[field].(map[string]any)
	if !ok {
		return nil, errors.New("native MCP server table is invalid")
	}
	return servers, nil
}

func equivalentDefinition(client string, a, b any) bool {
	// Claude treats a missing type as stdio when command is present, and HTTP
	// URL definitions are normalized by Skillshare to an explicit http type.
	normalize := func(value any) any {
		m, ok := value.(map[string]any)
		if !ok || client != "claude" {
			return value
		}
		copy := make(map[string]any, len(m)+1)
		for k, v := range m {
			copy[k] = v
		}
		if _, exists := copy["type"]; !exists {
			if _, exists := copy["command"]; exists {
				copy["type"] = "stdio"
			} else if _, exists := copy["url"]; exists {
				copy["type"] = "http"
			}
		}
		return copy
	}
	return equivalentJSON(normalize(a), normalize(b))
}

func equivalentJSON(a, b any) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && reflect.DeepEqual(left, right)
}
