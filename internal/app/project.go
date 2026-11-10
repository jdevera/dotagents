package app

import (
	"context"
	"fmt"
	"github.com/jdevera/dotagents/internal/skills"
	"os"
	"path/filepath"
)

type ProjectResult struct {
	Catalog     string   `json:"catalog"`
	Directory   string   `json:"directory"`
	Topic       string   `json:"topic"`
	Skills      []string `json:"skills"`
	Transaction string   `json:"transaction"`
}

func (a *App) Project(ctx context.Context, topic, directory string, targets []string) (ProjectResult, error) {
	result := ProjectResult{Topic: topic, Skills: []string{}}
	if !identifier.MatchString(topic) {
		return result, fmt.Errorf("invalid topic %q", topic)
	}
	if directory == "" {
		return result, fmt.Errorf("project requires --directory PATH")
	}
	if len(targets) == 0 {
		targets = []string{"codex", "claude"}
	}
	for _, target := range targets {
		if target != "codex" && target != "claude" {
			return result, fmt.Errorf("unsupported project target %q", target)
		}
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return result, err
	}
	info, err := os.Stat(directory)
	if err != nil {
		return result, err
	}
	if !info.IsDir() {
		return result, fmt.Errorf("project path must be a directory")
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return result, err
	}
	result.Directory = directory
	result.Catalog = filepath.Join(directory, ".dotagents-project")
	sourceRepo, err := filepath.EvalSymlinks(a.Repo)
	if err != nil {
		return result, err
	}
	if result.Catalog == sourceRepo {
		return result, fmt.Errorf("source catalog cannot also be the destination project catalog")
	}
	if info, err := os.Lstat(result.Catalog); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return result, fmt.Errorf("project catalog cannot be a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return result, err
	}
	var selected []Skill
	for _, entry := range a.Config.Skills {
		if contains(entry.Topics, topic) && a.wanted(entry.Groups, entry.When) {
			if entry.Owner == "native" {
				return result, fmt.Errorf("%s is native-owned and cannot be republished into a project", entry.ID)
			}
			selected = append(selected, entry)
		}
	}
	if len(selected) == 0 {
		return result, fmt.Errorf("topic %q has no skills matching this machine", topic)
	}
	var project *App
	if _, err := os.Lstat(filepath.Join(result.Catalog, "dotagents.json")); os.IsNotExist(err) {
		project = &App{Repo: result.Catalog, Home: directory, Machine: a.Machine, Config: Config{Version: 1, Scope: "project", Groups: a.Config.Groups, Skills: []Skill{}, Ignore: []Ignore{}, MCP: []MCPRule{}}}
	} else if err != nil {
		return result, err
	} else {
		project, err = Open(result.Catalog, directory)
		if err != nil {
			return result, err
		}
		if project.Config.Scope != "project" {
			return result, fmt.Errorf("destination already contains a non-project catalog")
		}
	}
	// An existing directory does not become ours just because an upstream skill
	// has the same name. Check ownership before writing the project catalog.
	claimed := map[string]string{}
	previous := map[string]Skill{}
	for _, entry := range project.Config.Skills {
		previous[entry.ID] = entry
		for _, loc := range entry.Locations {
			claimed[loc] = entry.ID
		}
	}
	for _, entry := range selected {
		candidateLocations := append([]string{}, previous[entry.ID].Locations...)
		for _, target := range targets {
			base := ".agents"
			if target == "claude" {
				base = ".claude"
			}
			loc := filepath.Join(base, "skills", entry.Name)
			if !contains(candidateLocations, loc) {
				candidateLocations = append(candidateLocations, loc)
			}
		}
		for _, loc := range candidateLocations {
			if owner := claimed[loc]; owner != "" && owner != entry.ID {
				return result, fmt.Errorf("project path %s already belongs to %s", loc, owner)
			}
			if _, err := os.Lstat(filepath.Join(directory, loc)); err == nil && claimed[loc] == "" {
				return result, fmt.Errorf("untracked project skill at %s; adopt or move it before installing this topic", loc)
			} else if err != nil && !os.IsNotExist(err) {
				return result, err
			}
			if oldID := claimed[loc]; oldID != "" {
				path := filepath.Join(directory, loc)
				if _, err := os.Lstat(path); err == nil {
					installed, err := skills.ReadTree(path)
					if err != nil {
						return result, err
					}
					accepted, err := project.Store().Load(previous[oldID].Snapshot)
					if err != nil {
						return result, err
					}
					if len(skills.Diff(accepted, installed)) > 0 {
						return result, fmt.Errorf("project skill %s has local changes; inspect and accept them before updating this topic", loc)
					}
				}
			}
		}
	}
	for _, entry := range selected {
		for _, group := range entry.Groups {
			project.Config.Groups[group] = a.Config.Groups[group]
		}
		tree, err := a.Store().Load(entry.Snapshot)
		if err != nil {
			return result, err
		}
		entry.Snapshot, err = project.Store().Save(tree)
		if err != nil {
			return result, err
		}
		entry.Scope = "project"
		entry.Locations = append([]string{}, previous[entry.ID].Locations...)
		for _, target := range targets {
			base := ".agents"
			if target == "claude" {
				base = ".claude"
			}
			loc := filepath.Join(base, "skills", entry.Name)
			if !contains(entry.Locations, loc) {
				entry.Locations = append(entry.Locations, loc)
			}
		}
		found := false
		for i, current := range project.Config.Skills {
			if current.ID == entry.ID {
				project.Config.Skills[i] = entry
				found = true
				break
			}
		}
		if !found {
			project.Config.Skills = append(project.Config.Skills, entry)
		}
		result.Skills = append(result.Skills, entry.ID)
	}
	if err := project.Save(); err != nil {
		return result, err
	}
	ignorePath := filepath.Join(result.Catalog, ".gitignore")
	if _, err := os.Lstat(ignorePath); os.IsNotExist(err) {
		if err := atomicWrite(ignorePath, []byte(".dotagents/\n"), 0644); err != nil {
			return result, err
		}
	} else if err != nil {
		return result, err
	}
	project, err = Open(result.Catalog, directory)
	if err != nil {
		return result, err
	}
	project.Machine = a.Machine
	project, err = project.Reload()
	if err != nil {
		return result, err
	}
	project.Only = append([]string{}, result.Skills...)
	plan, err := project.Plan()
	if err != nil {
		return result, err
	}
	if len(plan.Issues) > 0 {
		return result, fmt.Errorf("project catalog saved; installation blocked: %v", plan.Issues)
	}
	result.Transaction, err = project.Apply(ctx, plan.Revision)
	return result, err
}
