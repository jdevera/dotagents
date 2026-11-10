package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/jdevera/dotagents/internal/integrations"
)

func (a *App) Fetch(ctx context.Context, source, name, ref string, targets, groups []string) (Skill, error) {
	if !identifier.MatchString(name) {
		return Skill{}, fmt.Errorf("invalid skill name %q", name)
	}
	if err := a.Config.validateGroups(groups); err != nil {
		return Skill{}, err
	}
	for _, entry := range a.Config.Skills {
		if entry.ID == name {
			return Skill{}, fmt.Errorf("%s already tracked; use a separate catalog to inspect a new source version", name)
		}
	}
	if len(targets) == 0 {
		return Skill{}, fmt.Errorf("select --targets codex,claude (one or both)")
	}
	entry := Skill{ID: name, Name: name, Owner: "skillshare", Source: source, Groups: groups, Snapshot: strings.Repeat("0", 64)}
	for _, target := range targets {
		var path string
		switch target {
		case "codex":
			path = ".agents/skills/" + name
		case "claude":
			path = ".claude/skills/" + name
		default:
			return Skill{}, fmt.Errorf("unsupported target %q", target)
		}
		if !contains(entry.Locations, path) {
			entry.Locations = append(entry.Locations, path)
		}
	}
	candidate := a.Config
	candidate.Skills = append(append([]Skill{}, candidate.Skills...), entry)
	if err := candidate.Validate(); err != nil {
		return Skill{}, err
	}
	fetched, err := integrations.Fetch(ctx, source, name, ref)
	if err != nil {
		return Skill{}, err
	}
	entry.Snapshot, err = a.Store().Save(fetched.Files)
	if err != nil {
		return Skill{}, err
	}
	entry.Source = source + "@" + fetched.Commit
	a.Config.Skills = append(a.Config.Skills, entry)
	if err := a.Save(); err != nil {
		return Skill{}, err
	}
	return entry, nil
}
