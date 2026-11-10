// Package plugins reads native plugin declarations. Cache presence alone is
// never treated as evidence that a plugin is enabled.
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pelletier/go-toml/v2"
)

type Entry struct {
	Name     string `json:"name"`
	Client   string `json:"client"`
	Enabled  bool   `json:"enabled"`
	Version  string `json:"version,omitempty"`
	Path     string `json:"path,omitempty"`
	Evidence string `json:"evidence"`
}

type Inventory struct {
	Entries    []Entry  `json:"entries"`
	SkillRoots []string `json:"skill_roots"`
	Issues     []string `json:"issues"`
}

func Scan(home string) Inventory {
	r := Inventory{Entries: []Entry{}, SkillRoots: []string{}, Issues: []string{}}
	var claude struct {
		Enabled map[string]bool `json:"enabledPlugins"`
	}
	settings := filepath.Join(home, ".claude/settings.json")
	if b, err := os.ReadFile(settings); err == nil {
		if json.Unmarshal(b, &claude) != nil {
			r.Issues = append(r.Issues, "Cannot parse Claude plugin declarations.")
		}
	} else if !os.IsNotExist(err) {
		r.Issues = append(r.Issues, "Cannot read Claude plugin declarations.")
	}
	var installed struct {
		Plugins map[string][]struct{ Scope, InstallPath, Version string } `json:"plugins"`
	}
	registry := filepath.Join(home, ".claude/plugins/installed_plugins.json")
	if b, err := os.ReadFile(registry); err == nil {
		if json.Unmarshal(b, &installed) != nil {
			r.Issues = append(r.Issues, "Cannot parse Claude installed plugin registry.")
		}
	} else if !os.IsNotExist(err) {
		r.Issues = append(r.Issues, "Cannot read Claude installed plugin registry.")
	}
	for name, enabled := range claude.Enabled {
		entry := Entry{Name: name, Client: "claude", Enabled: enabled, Evidence: "user settings enabledPlugins; user-scoped installed plugin registry"}
		count := 0
		for _, record := range installed.Plugins[name] {
			if record.Scope == "user" {
				entry.Path = record.InstallPath
				entry.Version = record.Version
				count++
			}
		}
		if count > 1 {
			r.Issues = append(r.Issues, fmt.Sprintf("Claude plugin %s has multiple user installations; cannot select an active version.", name))
			entry.Path = ""
		}
		if enabled && entry.Path != "" {
			root := filepath.Join(entry.Path, "skills")
			if info, err := os.Stat(root); err == nil && info.IsDir() {
				r.SkillRoots = append(r.SkillRoots, root)
			} else if err != nil && !os.IsNotExist(err) {
				r.Issues = append(r.Issues, fmt.Sprintf("Cannot inspect Claude plugin skill directory for %s.", name))
			}
		} else if enabled && count == 0 {
			r.Issues = append(r.Issues, fmt.Sprintf("Claude plugin %s is enabled but has no user installation record.", name))
		}
		r.Entries = append(r.Entries, entry)
	}
	var codex struct {
		Plugins map[string]struct{ Enabled bool } `toml:"plugins"`
	}
	if b, err := os.ReadFile(filepath.Join(home, ".codex/config.toml")); err == nil {
		if toml.Unmarshal(b, &codex) != nil {
			r.Issues = append(r.Issues, "Cannot parse Codex plugin declarations.")
		} else {
			for name, setting := range codex.Plugins {
				r.Entries = append(r.Entries, Entry{Name: name, Client: "codex", Enabled: setting.Enabled, Evidence: "config.toml plugins declaration; active version is runtime-managed and not inferred from cache"})
			}
		}
	} else if !os.IsNotExist(err) {
		r.Issues = append(r.Issues, "Cannot read Codex plugin declarations.")
	}
	sort.Slice(r.Entries, func(i, j int) bool {
		a, b := r.Entries[i], r.Entries[j]
		if a.Client != b.Client {
			return a.Client < b.Client
		}
		return a.Name < b.Name
	})
	sort.Strings(r.SkillRoots)
	return r
}
