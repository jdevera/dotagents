package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jdevera/dotagents/internal/skills"
)

type ProjectResult struct {
	Config    string   `json:"config"`
	Source    string   `json:"source"`
	Directory string   `json:"directory"`
	Topic     string   `json:"topic"`
	Skills    []string `json:"skills"`
	Backups   []string `json:"backups"`
}

type nativeProject struct {
	Root       string   `json:"root"`
	OriginPath string   `json:"originPath"`
	Targets    []string `json:"targets"`
	Skills     *struct {
		Mode    string   `json:"mode"`
		Naming  string   `json:"targetNaming"`
		Include []string `json:"include"`
	} `json:"skills"`
	Agents json.RawMessage `json:"agents"`
	Groups []struct {
		Target string `json:"target"`
		Path   string `json:"skillsPath"`
	} `json:"groups"`
}

type nativeProjectPreview struct {
	Project nativeProject `json:"project"`
	Sync    struct {
		Results []struct {
			Target  string   `json:"target"`
			Linked  []string `json:"linked"`
			Updated []string `json:"updated"`
			Skipped []string `json:"skipped"`
			Pruned  []string `json:"pruned"`
		} `json:"results"`
		Failed          []json.RawMessage `json:"failed"`
		FolderConflicts []json.RawMessage `json:"folder_conflicts"`
		PathOverlap     int               `json:"path_overlap"`
	} `json:"sync"`
	Detach struct {
		Removed    []string `json:"removed"`
		CopiedBack []string `json:"copiedBack"`
	} `json:"detach"`
}

// Project selects a topic; Skillshare owns its registration, backups and links.
// Native source edits are deliberately not accepted into the audit catalog here.
func (a *App) Project(ctx context.Context, topic, directory string, targets []string) (ProjectResult, error) {
	result := ProjectResult{Topic: topic, Skills: []string{}, Backups: []string{}}
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
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return result, err
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return result, fmt.Errorf("project path must be an existing directory")
	}
	result.Directory = directory
	if _, err := os.Lstat(filepath.Join(directory, ".dotagents-project")); err == nil {
		return result, fmt.Errorf("legacy .dotagents-project exists; review and migrate it to native Skillshare before adding topics")
	} else if !os.IsNotExist(err) {
		return result, err
	}
	selected := []Skill{}
	for _, entry := range a.Config.Skills {
		if contains(entry.Topics, topic) && a.wanted(entry.Groups, entry.When) {
			if entry.Owner != "skillshare" {
				return result, fmt.Errorf("topic skill %s is owned by %s; project topics use the native Skillshare library", entry.ID, entry.Owner)
			}
			selected = append(selected, entry)
			result.Skills = append(result.Skills, entry.ID)
		}
	}
	if len(selected) == 0 {
		return result, fmt.Errorf("topic %q has no skills matching this machine", topic)
	}
	plan, err := a.NativeSkillsharePlan(ctx)
	if err != nil {
		return result, err
	}
	if !plan.Initialized {
		return result, fmt.Errorf("connect the native Skillshare library before adding project topics")
	}
	result.Source = plan.Source
	names := map[string]bool{}
	for _, entry := range selected {
		if !identifier.MatchString(entry.Name) {
			return result, fmt.Errorf("unsupported native library name %q", entry.Name)
		}
		if names[entry.Name] {
			return result, fmt.Errorf("multiple selected catalog entries claim native skill %s", entry.Name)
		}
		names[entry.Name] = true
		tree, err := skills.ReadTree(filepath.Join(plan.Source, entry.Name))
		if err != nil {
			return result, fmt.Errorf("install %s into the native Skillshare library first: %w", entry.Name, err)
		}
		if _, exists := tree["SKILL.md"]; !exists {
			return result, fmt.Errorf("native library entry %s is not a skill", entry.Name)
		}
	}
	data, err := a.SkillshareCommand(ctx, "project", "list", "--json")
	if err != nil {
		return result, err
	}
	var list struct {
		Projects []nativeProject `json:"projects"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return result, fmt.Errorf("read native projects: %w", err)
	}
	var current *nativeProject
	for i := range list.Projects {
		root, err := filepath.EvalSymlinks(list.Projects[i].Root)
		if err == nil && root == directory {
			current = &list.Projects[i]
			break
		}
	}
	args := []string{"project", "add", directory, "--machine-local", "--no-agents"}
	includes := []string{}
	if current != nil {
		if current.Skills == nil || (len(current.Agents) > 0 && string(current.Agents) != "null") {
			return result, fmt.Errorf("this native project manages agents; configure it directly in Skillshare")
		}
		if current.Skills.Mode != "" && current.Skills.Mode != "merge" || current.Skills.Naming != "" && current.Skills.Naming != "flat" {
			return result, fmt.Errorf("this native project uses custom mode or naming; configure it directly in Skillshare")
		}
		args = []string{"project", "edit", directory}
		targets = append(append([]string{}, current.Targets...), targets...)
		includes = append(includes, current.Skills.Include...)
	}
	for _, target := range uniqueProjectValues(targets) {
		args = append(args, "--target", target)
	}
	// An empty existing filter deliberately means all skills; never narrow it.
	if current == nil || len(includes) > 0 {
		for _, entry := range selected {
			includes = append(includes, entry.Name)
		}
		for _, name := range uniqueProjectValues(includes) {
			args = append(args, "--skills-include", name)
		}
	}
	args = append(args, "--skills-mode", "merge", "--skills-naming", "flat", "--json")
	data, err = a.SkillshareCommand(ctx, append(append([]string{}, args...), "--dry-run", "--sync")...)
	if err != nil {
		return result, err
	}
	var preview nativeProjectPreview
	if err := json.Unmarshal(data, &preview); err != nil {
		return result, fmt.Errorf("read native project preview: %w", err)
	}
	if preview.Project.Root != directory || !filepath.IsAbs(preview.Project.OriginPath) {
		return result, fmt.Errorf("native preview did not identify the requested project and configuration")
	}
	if err := checkNativeProjectPreview(directory, plan.Source, selected, preview); err != nil {
		return result, err
	}
	result.Config = preview.Project.OriginPath
	if current != nil {
		backup, err := a.BackupNativeConfig(current.OriginPath)
		if err != nil {
			return result, err
		}
		if backup != "" {
			result.Backups = append(result.Backups, backup)
		}
	}
	if _, err := a.SkillshareCommand(ctx, args...); err != nil {
		return result, err
	}
	for _, group := range preview.Project.Groups {
		backup, err := a.SkillshareCommand(ctx, "backup", "--target", group.Target)
		if err != nil {
			return result, fmt.Errorf("native project registered but backup failed; no sync performed: %w", err)
		}
		result.Backups = append(result.Backups, strings.TrimSpace(string(backup)))
	}
	if _, err := a.SkillshareCommand(ctx, "project", "sync", directory, "--json"); err != nil {
		return result, fmt.Errorf("native project registered; Skillshare sync failed: %w", err)
	}
	for _, group := range preview.Project.Groups {
		for _, entry := range selected {
			if err := checkNativeProjectLink(group.Path, entry.Name, filepath.Join(plan.Source, entry.Name), false); err != nil {
				return result, fmt.Errorf("native sync verification: %w", err)
			}
		}
	}
	return result, nil
}

func uniqueProjectValues(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		seen[value] = true
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func checkNativeProjectLink(target, name, source string, allowAbsent bool) error {
	path := filepath.Join(target, name)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && allowAbsent {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("untracked project skill at %s; move or import it with Skillshare first", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	expected, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	if resolved != expected {
		return fmt.Errorf("project skill %s links outside its native source; refusing to replace it", path)
	}
	return nil
}

func checkNativeProjectPreview(directory, source string, selected []Skill, preview nativeProjectPreview) error {
	if len(preview.Project.Groups) == 0 || len(preview.Sync.Results) == 0 {
		return fmt.Errorf("native preview did not report project skill targets")
	}
	if len(preview.Sync.Failed)+len(preview.Sync.FolderConflicts)+len(preview.Detach.Removed)+len(preview.Detach.CopiedBack) > 0 {
		return fmt.Errorf("native project preview contains conflicts or removals; inspect it in Skillshare")
	}
	for _, sync := range preview.Sync.Results {
		if len(sync.Pruned) > 0 {
			return fmt.Errorf("native sync would prune skills; inspect this project in Skillshare first")
		}
	}
	// path_overlap describes the whole native configuration, including global
	// cross-runtime discovery. Validate the actual project write paths instead.
	physicalTargets := []string{}
	physicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	for _, group := range preview.Project.Groups {
		if !filepath.IsAbs(group.Path) || !pathWithin(directory, group.Path) || group.Path == directory {
			return fmt.Errorf("native project target escapes the project: %s", group.Path)
		}
		physical, err := safeDestination(directory, filepath.Join(group.Path, ".dotagents-path-check"))
		if err != nil {
			return err
		}
		if pathWithin(physicalSource, physical) || pathWithin(physical, physicalSource) {
			return fmt.Errorf("native project target overlaps its source: %s", group.Path)
		}
		for _, previous := range physicalTargets {
			if pathWithin(previous, physical) || pathWithin(physical, previous) {
				return fmt.Errorf("native project targets overlap: %s", group.Path)
			}
		}
		physicalTargets = append(physicalTargets, physical)
		for _, sync := range preview.Sync.Results {
			if sync.Target != group.Target {
				continue
			}
			for _, name := range append(append([]string{}, sync.Linked...), sync.Updated...) {
				if !identifier.MatchString(name) {
					return fmt.Errorf("native preview has unsupported skill name %q", name)
				}
				if _, err := safeDestination(directory, filepath.Join(group.Path, name)); err != nil {
					return err
				}
				if err := checkNativeProjectLink(group.Path, name, filepath.Join(source, name), true); err != nil {
					return err
				}
			}
		}
		for _, entry := range selected {
			path := filepath.Join(group.Path, entry.Name)
			if _, err := safeDestination(directory, path); err != nil {
				return err
			}
			if err := checkNativeProjectLink(group.Path, entry.Name, filepath.Join(source, entry.Name), true); err != nil {
				return err
			}
			planned := false
			for _, sync := range preview.Sync.Results {
				if sync.Target == group.Target && (contains(sync.Linked, entry.Name) || contains(sync.Updated, entry.Name) || contains(sync.Skipped, entry.Name)) {
					planned = true
				}
			}
			if !planned {
				return fmt.Errorf("native filters exclude selected skill %s from %s", entry.Name, group.Target)
			}
		}
	}
	return nil
}
