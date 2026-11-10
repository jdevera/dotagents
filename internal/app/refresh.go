package app

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jdevera/dotagents/internal/integrations"
	"github.com/jdevera/dotagents/internal/skills"
)

type RefreshResult struct {
	ID       string          `json:"id"`
	Source   string          `json:"source"`
	Commit   string          `json:"commit"`
	Accepted bool            `json:"accepted"`
	Changes  []skills.Change `json:"changes"`
}

func (a *App) Refresh(ctx context.Context, id, ref string, accept bool) (RefreshResult, error) {
	result := RefreshResult{ID: id, Changes: []skills.Change{}}
	for i, entry := range a.Config.Skills {
		if entry.ID != id {
			continue
		}
		if entry.Owner != "skillshare" {
			return result, fmt.Errorf("%s is not Skillshare-owned; tool-generated skills use diff and accept --from tool", id)
		}
		source := entry.Source
		if at := strings.LastIndex(source, "@"); at >= 0 && regexp.MustCompile(`^[0-9a-fA-F]{40}$`).MatchString(source[at+1:]) {
			source = source[:at]
		}
		if source == "" {
			return result, fmt.Errorf("%s has no recorded upstream source", id)
		}
		if ref == "" {
			var err error
			ref, err = integrations.LatestCommit(ctx, source)
			if err != nil {
				return result, err
			}
		}
		fetched, err := integrations.Fetch(ctx, source, entry.Name, ref)
		if err != nil {
			return result, err
		}
		before, err := a.Store().Load(entry.Snapshot)
		if err != nil {
			return result, err
		}
		result.Source = source
		result.Commit = fetched.Commit
		result.Changes = skills.Diff(before, fetched.Files)
		if accept {
			snapshot, err := a.Store().Save(fetched.Files)
			if err != nil {
				return result, err
			}
			a.Config.Skills[i].Snapshot = snapshot
			a.Config.Skills[i].Source = source + "@" + fetched.Commit
			if err := a.Save(); err != nil {
				return result, err
			}
			result.Accepted = true
		}
		return result, nil
	}
	return result, fmt.Errorf("unknown skill %s", id)
}
