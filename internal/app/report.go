package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jdevera/dotagents/internal/mcp"
	"github.com/jdevera/dotagents/internal/plugins"
	"github.com/jdevera/dotagents/internal/providers"
	"github.com/jdevera/dotagents/internal/skills"
)

type Finding struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	Status               string          `json:"status"`
	Message              string          `json:"message"`
	Owner                string          `json:"owner"`
	Source               string          `json:"source,omitempty"`
	Locations            []string        `json:"locations"`
	InstalledChanges     []skills.Change `json:"installed_changes"`
	GeneratedChanges     []skills.Change `json:"generated_changes"`
	ToolInstalledChanges []skills.Change `json:"tool_installed_changes,omitempty"`
	ToolVersion          string          `json:"tool_version,omitempty"`
	ToolMethod           string          `json:"tool_method,omitempty"`
	ProviderError        string          `json:"provider_error,omitempty"`
}

type Report struct {
	Catalog     string            `json:"catalog"`
	Target      string            `json:"target"`
	Scope       string            `json:"scope"`
	Machine     Machine           `json:"machine"`
	GeneratedAt string            `json:"generated_at"`
	Inventory   skills.Inventory  `json:"inventory"`
	Findings    []Finding         `json:"findings"`
	MCP         mcp.Inventory     `json:"mcp"`
	Plugins     plugins.Inventory `json:"plugins"`
	Counts      map[string]int    `json:"counts"`
}

func (a *App) Store() *skills.Store { return skills.NewStore(filepath.Join(a.Repo, "catalog")) }

func (a *App) Report(ctx context.Context) Report {
	roots := make([]string, 0, len(a.Config.Roots))
	for _, root := range a.Config.Roots {
		roots = append(roots, expandHome(a.Home, root))
	}
	native := plugins.Scan(a.Home)
	roots = append(roots, native.SkillRoots...)
	mcpInventory := mcp.Scan(a.Home)
	if a.Config.Scope == "project" {
		mcpInventory = mcp.ScanProject(a.Home)
	}
	r := Report{Machine: a.Machine, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Inventory: skills.Scan(a.Home, roots), MCP: mcpInventory, Plugins: native, Findings: []Finding{}, Counts: map[string]int{}}
	r.Catalog = a.Repo
	r.Target = a.Home
	r.Scope = a.Config.Scope
	if r.Scope == "" {
		r.Scope = "home"
	}
	r.Inventory.Issues = append(r.Inventory.Issues, native.Issues...)
	for i, item := range r.Inventory.Items {
		for _, plugin := range native.Entries {
			if plugin.Path != "" && strings.HasPrefix(item.Path, plugin.Path+string(filepath.Separator)) {
				r.Inventory.Items[i].OwnerHint = "native-plugin"
				r.Inventory.Items[i].Source = plugin.Client + ":" + plugin.Name + "@" + plugin.Version
				break
			}
		}
	}
	claimed := map[string]bool{}
	for _, entry := range a.Config.Skills {
		f := Finding{ID: entry.ID, Name: entry.Name, Owner: entry.Owner, Source: entry.Source, Status: "ok", Message: "Installed contents match the accepted snapshot.", Locations: []string{}, InstalledChanges: []skills.Change{}, GeneratedChanges: []skills.Change{}}
		for _, loc := range entry.Locations {
			abs := filepath.Join(a.Home, loc)
			claimed[abs] = true
			f.Locations = append(f.Locations, abs)
		}
		wanted := a.skillWanted(entry)
		if !wanted {
			present := false
			var inspectErr error
			for _, path := range f.Locations {
				if _, err := os.Lstat(path); err == nil {
					present = true
				} else if !os.IsNotExist(err) {
					inspectErr = err
				}
			}
			if inspectErr != nil {
				f.Status = "unverified"
				f.Message = "Cannot inspect unselected location: " + inspectErr.Error()
			} else if present {
				f.Status = "unexpected"
				f.Message = "Installed here, but this machine does not match its rules. Left untouched."
			} else {
				f.Status = "inactive"
				f.Message = "Not selected for this machine."
				if entry.Scope == "project" && a.Config.Scope != "project" {
					f.Status = "available"
					f.Message = "Saved in the catalog for project installation; not selected globally."
				}
			}
			r.Findings = append(r.Findings, f)
			continue
		}
		accepted, err := a.Store().Load(entry.Snapshot)
		if err != nil {
			f.Status = "unverified"
			f.Message = "Cannot load accepted snapshot: " + err.Error()
			r.Findings = append(r.Findings, f)
			continue
		}
		var first skills.Tree
		for _, path := range f.Locations {
			installed, err := skills.ReadTree(path)
			if err != nil {
				if _, statErr := os.Lstat(path); os.IsNotExist(statErr) {
					if f.Status == "ok" {
						f.Status = "missing"
					}
					f.Message = "One or more selected installations are missing."
				} else {
					f.Status = "unverified"
					f.Message = fmt.Sprintf("Cannot inspect %s: %v", path, err)
				}
				continue
			}
			if first == nil {
				first = installed
			}
			changes := skills.Diff(accepted, installed)
			if len(changes) > 0 {
				if f.Status == "ok" {
					f.Status = "changed"
					f.Message = "Installed contents differ from the accepted snapshot."
				}
				for _, change := range changes {
					change.Path = path + "/" + change.Path
					f.InstalledChanges = append(f.InstalledChanges, change)
				}
			}
		}
		if entry.Owner == "td" || entry.Owner == "jev" || strings.HasPrefix(entry.Owner, "tool:") {
			result, err := providers.Probe(ctx, strings.TrimPrefix(entry.Owner, "tool:"), providers.Options{Home: a.Machine.Home, Path: entry.ProviderPath})
			if err != nil {
				f.ProviderError = err.Error()
				if f.Status == "ok" {
					f.Status = "unverified"
					f.Message = "Installed contents match, but current tool output could not be compared."
				}
			} else {
				f.ToolVersion = result.Version
				f.ToolMethod = result.Method
				f.GeneratedChanges = skills.Diff(accepted, result.Files)
				if first != nil {
					f.ToolInstalledChanges = skills.Diff(first, result.Files)
				}
				if len(f.GeneratedChanges) > 0 && f.Status == "ok" {
					f.Status = "tool_changed"
					f.Message = "Installed contents match the accepted snapshot; the current tool produces different contents."
				}
			}
		}
		r.Findings = append(r.Findings, f)
	}
	for _, item := range r.Inventory.Items {
		var unknown []string
		for _, loc := range item.Locations {
			if !claimed[loc] {
				unknown = append(unknown, loc)
			}
		}
		if len(unknown) == 0 {
			continue
		}
		f := Finding{ID: item.ID, Name: item.Name, Owner: item.OwnerHint, Source: item.Source, Locations: unknown, Status: "unknown", Message: "Discovered outside the catalog. Left working; adopt it or record an ignore rule.", InstalledChanges: []skills.Change{}, GeneratedChanges: []skills.Change{}}
		allIgnored := true
		for _, loc := range unknown {
			ignored := false
			for _, rule := range a.Config.Ignore {
				p := filepath.Join(a.Home, rule.Path)
				if loc == p || strings.HasPrefix(loc, p+string(filepath.Separator)) {
					ignored = true
					break
				}
			}
			allIgnored = allIgnored && ignored
		}
		if allIgnored {
			f.Status = "ignored"
			f.Message = "Explicitly ignored by catalog rules; left untouched."
		}
		r.Findings = append(r.Findings, f)
	}
	for _, entry := range r.MCP.Entries {
		f := Finding{ID: "mcp:" + entry.Client + ":" + entry.Name, Name: entry.Name, Owner: "mcp", Locations: []string{entry.Path}, Status: "unknown", Message: "MCP definition is not tracked. Credentials are not displayed."}
		for _, rule := range a.Config.MCP {
			if rule.Name == entry.Name && rule.Client == entry.Client {
				if !a.wanted(rule.Groups, nil) {
					f.Status = "unexpected"
					f.Message = "MCP exists on a machine outside its selected groups."
				} else if expectedMCPFingerprint(rule) != entry.Fingerprint {
					f.Status = "changed"
					f.Message = "MCP configuration changed, including possibly credential values. Contents are not displayed."
				} else {
					f.Status = "ok"
					f.Message = "MCP configuration matches its accepted fingerprint."
				}
				if expectedMCPFingerprint(rule) != entry.Fingerprint {
					before, readErr := a.acceptedMCP(rule)
					after, currentErr := a.currentMCP(entry.Client, entry.Name)
					if readErr == nil && currentErr == nil {
						if mcp.Equivalent(entry.Client, before, after) {
							if f.Status == "changed" {
								f.Status = "ok"
								f.Message = "MCP definition matches; only an equivalent native default differs."
							}
						} else {
							f.InstalledChanges = mcp.DefinitionDiffFor(entry.Client, before, after)
						}
					} else {
						f.Message += " Detailed comparison unavailable: accepted private snapshot or current definition could not be read."
					}
				}
				break
			}
		}
		r.Findings = append(r.Findings, f)
	}
	for _, rule := range a.Config.MCP {
		if !a.wanted(rule.Groups, nil) {
			continue
		}
		found := false
		for _, entry := range r.MCP.Entries {
			if entry.Name == rule.Name && entry.Client == rule.Client {
				found = true
			}
		}
		if !found {
			r.Findings = append(r.Findings, Finding{ID: "mcp:" + rule.Client + ":" + rule.Name, Name: rule.Name, Owner: "mcp", Status: "missing", Message: "Accepted MCP definition is missing."})
		}
	}
	sort.SliceStable(r.Findings, func(i, j int) bool { return r.Findings[i].ID < r.Findings[j].ID })
	for _, f := range r.Findings {
		r.Counts[f.Status]++
	}
	if len(r.Inventory.Issues)+len(r.MCP.Issues) > 0 {
		r.Counts["unverified"] += len(r.Inventory.Issues) + len(r.MCP.Issues)
	}
	return r
}

func (r Report) NeedsAttention() bool {
	for key, count := range r.Counts {
		if count > 0 && key != "ok" && key != "inactive" && key != "ignored" && key != "available" {
			return true
		}
	}
	return false
}

func (a *App) Adopt(path, id, owner, source string, groups []string) (Skill, error) {
	path = expandHome(a.Home, path)
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Skill{}, err
	}
	rel, err := filepath.Rel(a.Home, absolute)
	if err != nil {
		return Skill{}, err
	}
	if err = validateLocation(rel); err != nil {
		return Skill{}, err
	}
	if id == "" {
		id = filepath.Base(absolute)
	}
	for _, entry := range a.Config.Skills {
		if entry.ID == id {
			return Skill{}, fmt.Errorf("%s already tracked; use accept to review a new snapshot", id)
		}
	}
	if owner == "" {
		owner = "manual"
	}
	entry := Skill{ID: id, Name: filepath.Base(absolute), Owner: owner, Source: source, Locations: []string{rel}, Groups: groups, Snapshot: strings.Repeat("0", 64)}
	// Validate before creating any snapshot files.
	candidate := a.Config
	candidate.Skills = append(append([]Skill{}, candidate.Skills...), entry)
	if err := candidate.Validate(); err != nil {
		return Skill{}, err
	}
	tree, err := skills.ReadTree(absolute)
	if err != nil {
		return Skill{}, err
	}
	if _, ok := tree["SKILL.md"]; !ok {
		return Skill{}, fmt.Errorf("%s has no SKILL.md", absolute)
	}
	entry.Snapshot, err = a.Store().Save(tree)
	if err != nil {
		return Skill{}, err
	}
	a.Config.Skills = append(a.Config.Skills, entry)
	if err = a.Save(); err != nil {
		return Skill{}, err
	}
	return entry, nil
}

func (a *App) Accept(ctx context.Context, id, from string) error {
	for i, entry := range a.Config.Skills {
		if entry.ID == id {
			var tree skills.Tree
			var err error
			switch from {
			case "installed":
				for _, loc := range entry.Locations {
					next, e := skills.ReadTree(filepath.Join(a.Home, loc))
					if e != nil {
						return e
					}
					if tree != nil && len(skills.Diff(tree, next)) > 0 {
						return fmt.Errorf("installations disagree; resolve the differences before accepting %s", id)
					}
					tree = next
				}
			case "tool":
				result, e := providers.Probe(ctx, strings.TrimPrefix(entry.Owner, "tool:"), providers.Options{Home: a.Machine.Home, Path: entry.ProviderPath})
				err = e
				tree = result.Files
				if e == nil {
					a.Config.Skills[i].Source = "tool:" + result.Tool + "@" + result.Version
				}
			default:
				return fmt.Errorf("--from must be installed or tool")
			}
			if err != nil {
				return err
			}
			snapshot, err := a.Store().Save(tree)
			if err != nil {
				return err
			}
			a.Config.Skills[i].Snapshot = snapshot
			return a.Save()
		}
	}
	return fmt.Errorf("unknown skill %s", id)
}
