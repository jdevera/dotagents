// Package mcp inventories native MCP configuration without starting servers,
// refreshing credentials, or exposing configuration values in its report.
package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pelletier/go-toml/v2"
)

type Entry struct {
	Name        string `json:"name"`
	Client      string `json:"client"`
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	Summary     string `json:"summary"`
	Enabled     bool   `json:"enabled"`
}

type Inventory struct {
	Entries []Entry  `json:"entries"`
	Issues  []string `json:"issues"`
	Roots   []string `json:"roots"`
}

// Scan covers global Claude Code and Codex server definitions. Plugin-managed
// servers, account-side integrations, and project settings are not inferred from
// caches. Roots makes the observed boundary visible to the caller.
func Scan(home string) Inventory {
	return scan(home, ".claude.json")
}

// ScanProject inventories project-local definitions without reading global
// agent settings or inferring globally inherited server availability.
func ScanProject(project string) Inventory {
	return scan(project, ".mcp.json")
}

func scan(home, claudeFile string) Inventory {
	result := Inventory{Entries: []Entry{}, Issues: []string{}, Roots: []string{}}
	for _, source := range []struct {
		client, relative, field string
		toml                    bool
	}{
		{"claude", claudeFile, "mcpServers", false},
		{"codex", ".codex/config.toml", "mcp_servers", true},
	} {
		path := filepath.Join(home, source.relative)
		result.Roots = append(result.Roots, path)
		data, err := readConfig(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			result.Issues = append(result.Issues, fmt.Sprintf("%s: cannot read configuration", path))
			continue
		}
		var document map[string]any
		if source.toml {
			err = toml.Unmarshal(data, &document)
		} else {
			err = json.Unmarshal(data, &document)
		}
		if err != nil || document == nil {
			// Parser errors can echo source values, including credentials.
			result.Issues = append(result.Issues, fmt.Sprintf("%s: invalid configuration; contents withheld", path))
			continue
		}
		raw, exists := document[source.field]
		if !exists {
			continue
		}
		servers, ok := raw.(map[string]any)
		if !ok {
			result.Issues = append(result.Issues, fmt.Sprintf("%s: %s must be an object", path, source.field))
			continue
		}
		for name, raw := range servers {
			config, ok := raw.(map[string]any)
			if !ok {
				result.Issues = append(result.Issues, fmt.Sprintf("%s: MCP %q is not an object", path, name))
				continue
			}
			canonical, err := json.Marshal(config)
			if err != nil {
				result.Issues = append(result.Issues, fmt.Sprintf("%s: MCP %q cannot be fingerprinted", path, name))
				continue
			}
			hash := sha256.Sum256(canonical)
			entry := Entry{Name: name, Client: source.client, Path: path, Fingerprint: hex.EncodeToString(hash[:]), Enabled: true}
			if enabled, ok := config["enabled"].(bool); ok && !enabled {
				entry.Enabled = false
			}
			if disabled, ok := config["disabled"].(bool); ok && disabled {
				entry.Enabled = false
			}
			switch {
			case config["url"] != nil:
				entry.Summary = "HTTP connection · settings hidden"
			case config["command"] != nil:
				entry.Summary = "Local command · settings hidden"
			default:
				entry.Summary = "Connection settings hidden"
			}
			if !entry.Enabled {
				entry.Summary = "Disabled · " + entry.Summary
			}
			result.Entries = append(result.Entries, entry)
		}
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		if result.Entries[i].Client != result.Entries[j].Client {
			return result.Entries[i].Client < result.Entries[j].Client
		}
		return result.Entries[i].Name < result.Entries[j].Name
	})
	sort.Strings(result.Issues)
	return result
}

func readConfig(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return nil, fmt.Errorf("unsupported configuration file")
	}
	return os.ReadFile(path)
}
