package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jdevera/dotagents/internal/mcp"
)

func (a *App) readPrivateMCP(hash string) (map[string]any, error) {
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash) {
		return nil, fmt.Errorf("invalid MCP fingerprint")
	}
	data, err := os.ReadFile(filepath.Join(a.Repo, ".dotagents", "private", "mcp", hash+".json"))
	if err != nil {
		return nil, err
	}
	var definition map[string]any
	if err = json.Unmarshal(data, &definition); err != nil {
		return nil, err
	}
	actual, err := mcp.Fingerprint(definition)
	if err != nil {
		return nil, err
	}
	if actual != hash {
		return nil, fmt.Errorf("accepted MCP snapshot was modified")
	}
	return definition, nil
}

func (a *App) TrackMCP(client, name string, groups []string) error {
	if err := a.Config.validateGroups(groups); err != nil {
		return err
	}
	definition, err := a.currentMCP(client, name)
	if err != nil {
		return err
	}
	hash, err := mcp.Fingerprint(definition)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(definition, "", "  ")
	if err != nil {
		return err
	}
	// Full MCP definitions can contain credentials. They are never put in catalog/.
	if err = atomicWrite(filepath.Join(a.Repo, ".dotagents", "private", "mcp", hash+".json"), append(data, '\n'), 0600); err != nil {
		return err
	}
	rule := MCPRule{Name: name, Client: client, Fingerprint: hash, Groups: groups, Definition: mcp.Redact(definition)}
	found := false
	for i, existing := range a.Config.MCP {
		if existing.Name == name && existing.Client == client {
			rule.Owner = existing.Owner
			a.Config.MCP[i] = rule
			found = true
			break
		}
	}
	if !found {
		a.Config.MCP = append(a.Config.MCP, rule)
	}
	return a.Save()
}

func (a *App) currentMCP(client, name string) (map[string]any, error) {
	if a.Config.Scope == "project" {
		return mcp.ReadProjectDefinition(a.Home, client, name)
	}
	return mcp.ReadDefinition(a.Home, client, name)
}
