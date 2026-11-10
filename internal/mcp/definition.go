package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jdevera/dotagents/internal/skills"
	"github.com/pelletier/go-toml/v2"
)

// ReadDefinition returns the actual native settings, including secrets. It is
// for an explicit private snapshot operation, never for a report or HTTP API.
// Callers must keep its output in a private, ignored store with mode 0600.
func ReadDefinition(home, client, name string) (map[string]any, error) {
	return readDefinition(home, client, name, ".claude.json")
}

// ReadProjectDefinition has the same private-data contract as ReadDefinition.
// Claude project definitions come from .mcp.json, never the global user file.
func ReadProjectDefinition(project, client, name string) (map[string]any, error) {
	return readDefinition(project, client, name, ".mcp.json")
}

func readDefinition(home, client, name, claudeFile string) (map[string]any, error) {
	var relative, field string
	switch client {
	case "claude":
		relative, field = claudeFile, "mcpServers"
	case "codex":
		relative, field = ".codex/config.toml", "mcp_servers"
	default:
		return nil, fmt.Errorf("unsupported MCP client %q", client)
	}
	data, err := readConfig(filepath.Join(home, relative))
	if err != nil {
		return nil, fmt.Errorf("cannot read %s MCP configuration", client)
	}
	var document map[string]any
	if client == "codex" {
		err = toml.Unmarshal(data, &document)
	} else {
		err = json.Unmarshal(data, &document)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid %s configuration; contents withheld", client)
	}
	servers, ok := document[field].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s has no MCP server object", client)
	}
	definition, ok := servers[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("MCP %q is absent or invalid in %s", name, client)
	}
	return definition, nil
}

// Fingerprint covers every value, including fields redacted from human output.
func Fingerprint(definition map[string]any) (string, error) {
	canonical, err := json.Marshal(definition)
	if err != nil {
		return "", errors.New("MCP definition cannot be encoded; contents withheld")
	}
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:]), nil
}

const hidden = "[hidden]"

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var envReference = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)
var flagName = regexp.MustCompile(`^--?[A-Za-z][A-Za-z0-9-]*$`)

// Redact returns a detached view suitable for display or a public catalog.
// Unknown fields are redacted by default; a key not resembling "token" is not
// proof of safety. Commands and argument values may contain inline credentials.
func Redact(definition map[string]any) map[string]any {
	if definition == nil {
		return nil
	}
	result := make(map[string]any, len(definition))
	for key, value := range definition {
		result[key] = redactValue(key, value, true)
	}
	return result
}

func redactValue(key string, value any, top bool) any {
	if top {
		switch key {
		case "enabled", "disabled", "required":
			if b, ok := value.(bool); ok {
				return b
			}
		case "startup_timeout_sec", "tool_timeout_sec", "timeout", "timeout_ms":
			switch value.(type) {
			case float64, int64, int:
				return value
			}
		case "type", "transport":
			if s, ok := value.(string); ok && (s == "stdio" || s == "http" || s == "sse" || s == "streamable-http") {
				return s
			}
		case "bearer_token_env_var":
			if s, ok := value.(string); ok && envName.MatchString(s) {
				return s
			}
		case "args":
			if args, ok := value.([]any); ok {
				result := make([]any, len(args))
				for i, arg := range args {
					result[i] = hidden
					if s, ok := arg.(string); ok && (flagName.MatchString(s) || envReference.MatchString(s)) {
						result[i] = s
					}
				}
				return result
			}
		}
	}
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for k, child := range v {
			result[k] = redactValue(k, child, false)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, child := range v {
			result[i] = redactValue("", child, false)
		}
		return result
	case []string:
		result := make([]string, len(v))
		for i := range v {
			result[i] = hidden
		}
		return result
	case string:
		if envReference.MatchString(v) {
			return v
		}
		return hidden
	case nil:
		return nil
	default:
		return hidden
	}
}

// DefinitionDiff shows safe structural differences. Even when redaction makes
// the displayed objects equal, changed private values produce a visible change.
func DefinitionDiff(before, after map[string]any) []skills.Change {
	oldRaw, oldErr := json.Marshal(before)
	newRaw, newErr := json.Marshal(after)
	if oldErr != nil || newErr != nil {
		return []skills.Change{{Path: "definition.json", Kind: "error", Diff: "Could not compare MCP definition; contents withheld."}}
	}
	if bytes.Equal(oldRaw, newRaw) {
		return nil
	}
	oldSafe, _ := json.MarshalIndent(Redact(before), "", "  ")
	newSafe, _ := json.MarshalIndent(Redact(after), "", "  ")
	changes := skills.Diff(
		skills.Tree{"definition.json": skills.File{Data: append(oldSafe, '\n'), Mode: 0600}},
		skills.Tree{"definition.json": skills.File{Data: append(newSafe, '\n'), Mode: 0600}},
	)
	if len(changes) == 0 {
		return []skills.Change{{Path: "definition.json", Kind: "modified", Diff: "MCP configuration changed in values hidden to protect credentials.\n"}}
	}
	for i := range changes {
		changes[i].Diff = strings.TrimRight(changes[i].Diff, "\n") + "\nPrivate values are hidden; the full definition fingerprint also changed.\n"
	}
	return changes
}

// Equivalent accounts only for native Claude transport defaults that
// Skillshare makes explicit when publishing. Exact raw snapshot integrity still
// uses Fingerprint. No other fields, including credential values, are ignored.
func Equivalent(client string, before, after map[string]any) bool {
	a, errA := json.Marshal(canonicalDefinition(client, before))
	b, errB := json.Marshal(canonicalDefinition(client, after))
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func DefinitionDiffFor(client string, before, after map[string]any) []skills.Change {
	return DefinitionDiff(canonicalDefinition(client, before), canonicalDefinition(client, after))
}

func canonicalDefinition(client string, definition map[string]any) map[string]any {
	if client != "claude" || definition == nil {
		return definition
	}
	result := make(map[string]any, len(definition)+1)
	for key, value := range definition {
		result[key] = value
	}
	if _, exists := result["type"]; !exists {
		if _, exists := result["command"]; exists {
			result["type"] = "stdio"
		} else if _, exists := result["url"]; exists {
			result["type"] = "http"
		}
	}
	return result
}
