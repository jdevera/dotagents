package app

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/jdevera/dotagents/internal/providers"
)

func (a *App) AddTool(ctx context.Context, tool, path string, targets, groups []string) (Skill, error) {
	if err := a.Config.validateGroups(groups); err != nil {
		return Skill{}, err
	}
	name := ""
	switch tool {
	case "td":
		name = "todoist-cli"
	case "jev":
		name = "jev-cli"
	default:
		return Skill{}, fmt.Errorf("no safe output adapter for %q; adopt an existing installation with --owner tool:%s to track it without claiming a generator comparison", tool, tool)
	}
	for _, entry := range a.Config.Skills {
		if entry.ID == name {
			return Skill{}, fmt.Errorf("%s already tracked; use diff and accept --from tool", name)
		}
	}
	if len(targets) == 0 {
		targets = []string{"codex", "claude"}
	}
	entry := Skill{ID: name, Name: name, Owner: tool, Groups: groups, ProviderPath: path}
	for _, target := range targets {
		base := ".agents"
		if target == "claude" {
			base = ".claude"
		} else if target != "codex" {
			return Skill{}, fmt.Errorf("unsupported target %q", target)
		}
		loc := filepath.Join(base, "skills", name)
		if !contains(entry.Locations, loc) {
			entry.Locations = append(entry.Locations, loc)
		}
	}
	result, err := providers.Probe(ctx, tool, providers.Options{Home: a.Machine.Home, Path: path})
	if err != nil {
		return Skill{}, err
	}
	entry.Source = "tool:" + tool + "@" + result.Version
	entry.Snapshot, err = a.Store().Save(result.Files)
	if err != nil {
		return Skill{}, err
	}
	a.Config.Skills = append(a.Config.Skills, entry)
	if err := a.Save(); err != nil {
		return Skill{}, err
	}
	return entry, nil
}
