package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Inventory struct {
	Items  []Item   `json:"items"`
	Issues []string `json:"issues"`
	Roots  []string `json:"roots"`
}

type Item struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	ResolvedPath string   `json:"resolved_path"`
	Source       string   `json:"source,omitempty"`
	OwnerHint    string   `json:"owner_hint,omitempty"`
	Locations    []string `json:"locations"`
}

// Scan inspects known skill directories. It does not interpret plugin caches as
// active installations and never invokes an owning tool.
func Scan(home string, extra []string) Inventory {
	inv := Inventory{Items: []Item{}, Issues: []string{}, Roots: []string{}}
	home, err := filepath.Abs(home)
	if err != nil {
		inv.Issues = append(inv.Issues, err.Error())
		return inv
	}
	roots := []string{filepath.Join(home, ".agents", "skills"), filepath.Join(home, ".codex", "skills"), filepath.Join(home, ".claude", "skills")}
	for _, root := range extra {
		if strings.HasPrefix(root, "~/") {
			root = filepath.Join(home, root[2:])
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			inv.Issues = append(inv.Issues, fmt.Sprintf("scan root %s: %v", root, err))
			continue
		}
		roots = append(roots, absolute)
	}
	sources := lockSources(home, &inv)
	byResolved := map[string]int{}
	seenRoots := map[string]bool{}
	var walked int
	var visit func(string, map[string]bool, int)
	visit = func(dir string, ancestors map[string]bool, depth int) {
		walked++
		if walked > 100000 {
			if walked == 100001 {
				inv.Issues = append(inv.Issues, "inventory exceeds 100000 directories; scan incomplete")
			}
			return
		}
		if depth > maxDepth {
			inv.Issues = append(inv.Issues, "inventory nesting limit reached: "+dir)
			return
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			inv.Issues = append(inv.Issues, fmt.Sprintf("resolve %s: %v", dir, err))
			return
		}
		if ancestors[resolved] {
			inv.Issues = append(inv.Issues, "directory link cycle: "+dir)
			return
		}
		info, err := os.Stat(resolved)
		if err != nil {
			inv.Issues = append(inv.Issues, fmt.Sprintf("inspect %s: %v", dir, err))
			return
		}
		if !info.IsDir() {
			return
		}
		marker := filepath.Join(dir, "SKILL.md")
		info, err = os.Stat(marker)
		if err == nil && info.Mode().IsRegular() {
			if index, ok := byResolved[resolved]; ok {
				item := &inv.Items[index]
				if !contains(item.Locations, dir) {
					item.Locations = append(item.Locations, dir)
				}
				return
			}
			id := dir
			if within(home, dir) {
				id, _ = filepath.Rel(home, dir)
				id = filepath.ToSlash(id)
			}
			item := Item{ID: id, Name: filepath.Base(dir), Path: dir, ResolvedPath: resolved, Locations: []string{dir}}
			item.Source = sources[resolved]
			if item.Source != "" {
				item.OwnerHint = "skills-cli"
			}
			if within(filepath.Join(home, ".codex", "skills", ".system"), dir) {
				item.OwnerHint = "codex-system"
			}
			if within(filepath.Join(home, ".claude", "skills", "synced"), dir) {
				item.OwnerHint = "claude-sync"
			}
			for _, name := range []string{".jev-cli-managed"} {
				if data, err := readBounded(filepath.Join(dir, name), 4096); err == nil && strings.Contains(string(data), "jev install-skills") {
					item.OwnerHint = "jev"
				}
			}
			byResolved[resolved] = len(inv.Items)
			inv.Items = append(inv.Items, item)
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			inv.Issues = append(inv.Issues, fmt.Sprintf("inspect %s: %v", marker, err))
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			inv.Issues = append(inv.Issues, fmt.Sprintf("read %s: %v", dir, err))
			return
		}
		ancestors[resolved] = true
		defer delete(ancestors, resolved)
		for _, entry := range entries {
			if entry.Name() == ".git" || entry.Name() == "node_modules" {
				continue
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				visit(filepath.Join(dir, entry.Name()), ancestors, depth+1)
			}
		}
	}
	for i, root := range roots {
		if seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		inv.Roots = append(inv.Roots, root)
		if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) && i < 3 {
			continue
		}
		visit(root, map[string]bool{}, 0)
	}
	for i := range inv.Items {
		sort.Strings(inv.Items[i].Locations)
	}
	sort.Slice(inv.Items, func(i, j int) bool { return inv.Items[i].ID < inv.Items[j].ID })
	sort.Strings(inv.Issues)
	return inv
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func lockSources(home string, inv *Inventory) map[string]string {
	result := map[string]string{}
	filename := filepath.Join(home, ".agents", ".skill-lock.json")
	data, err := readBounded(filename, 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return result
	}
	if err != nil {
		inv.Issues = append(inv.Issues, fmt.Sprintf("read source evidence %s: %v", filename, err))
		return result
	}
	var lock struct {
		Skills map[string]struct {
			Source    string `json:"source"`
			SourceURL string `json:"sourceUrl"`
		} `json:"skills"`
	}
	if err = json.Unmarshal(data, &lock); err != nil {
		inv.Issues = append(inv.Issues, fmt.Sprintf("parse source evidence %s: %v", filename, err))
		return result
	}
	for name, source := range lock.Skills {
		if safeName(name) != nil {
			inv.Issues = append(inv.Issues, "invalid skill path in source evidence: "+name)
			continue
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(home, ".agents", "skills", filepath.FromSlash(name)))
		if err != nil {
			continue
		}
		result[resolved] = source.SourceURL
		if result[resolved] == "" {
			result[resolved] = source.Source
		}
	}
	return result
}
