package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"

	"github.com/jdevera/dotagents/internal/mcp"
)

// A portable definition is deliberately authored, unlike an automatic private
// capture. The redacted Definition field is never used as executable settings.
func (a *App) acceptedMCP(rule MCPRule) (map[string]any, error) {
	if rule.Template != nil {
		return rule.Template, nil
	}
	return a.readPrivateMCP(rule.Fingerprint)
}

func expectedMCPFingerprint(rule MCPRule) string {
	if rule.Template != nil {
		hash, _ := mcp.Fingerprint(rule.Template)
		return hash
	}
	return rule.Fingerprint
}

func (a *App) DefineMCP(client, name, file string, groups []string) error {
	if client != "claude" && client != "codex" {
		return fmt.Errorf("client must be claude or codex")
	}
	if name == "" {
		return fmt.Errorf("MCP name is required")
	}
	if err := a.Config.validateGroups(groups); err != nil {
		return err
	}
	input, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return fmt.Errorf("definition must be a regular file no larger than 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("definition exceeds 1 MiB")
	}
	var definition map[string]any
	if err = json.Unmarshal(data, &definition); err != nil {
		return fmt.Errorf("invalid MCP definition JSON")
	}
	if len(definition) == 0 {
		return fmt.Errorf("empty MCP definition")
	}
	if err := validatePortableMCP(definition); err != nil {
		return err
	}
	hash, err := mcp.Fingerprint(definition)
	if err != nil {
		return err
	}
	rule := MCPRule{Name: name, Client: client, Fingerprint: hash, Groups: groups, Definition: mcp.Redact(definition), Template: definition}
	found := false
	for i, old := range a.Config.MCP {
		if old.Client == client && old.Name == name {
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

func validatePortableMCP(def map[string]any) error {
	if command, _ := def["command"].(string); command == "" {
		if address, _ := def["url"].(string); address == "" {
			return fmt.Errorf("definition needs command or url")
		}
	}
	ref := regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)
	var visit func(string, any) error
	visit = func(key string, value any) error {
		lower := strings.ToLower(key)
		switch v := value.(type) {
		case map[string]any:
			for k, item := range v {
				if lower == "env_http_headers" {
					s, ok := item.(string)
					if !ok || !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(s) {
						return fmt.Errorf("env_http_headers values must be environment variable names")
					}
					continue
				}
				if lower == "env" || lower == "headers" || lower == "http_headers" {
					s, ok := item.(string)
					if !ok || !ref.MatchString(s) {
						return fmt.Errorf("portable definitions require environment references in %s; use native env_vars, env_http_headers or bearer_token_env_var where supported", key)
					}
				}
				if err := visit(k, item); err != nil {
					return err
				}
			}
		case []any:
			for i, item := range v {
				if lower == "args" {
					if s, ok := item.(string); ok {
						l := strings.ToLower(s)
						for _, word := range []string{"token", "password", "secret", "api-key", "api_key", "authorization"} {
							if strings.Contains(l, word) {
								if strings.Contains(s, "=") || i+1 < len(v) {
									return fmt.Errorf("credential-like command arguments must stay outside the versioned definition")
								}
							}
						}
					}
				}
				if err := visit(key, item); err != nil {
					return err
				}
			}
		case string:
			if lower == "url" {
				u, err := url.Parse(v)
				if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
					return fmt.Errorf("portable MCP URL must not include credentials, query parameters or fragments")
				}
			}
			if strings.HasSuffix(lower, "_env_var") && !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(v) {
				return fmt.Errorf("%s must name an environment variable", key)
			}
			sensitive := strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || lower == "authorization" || lower == "api_key"
			if sensitive && !strings.HasSuffix(lower, "_env_var") && !ref.MatchString(v) {
				return fmt.Errorf("literal credential-like value in %s; use a native environment reference", key)
			}
		}
		return nil
	}
	return visit("", def)
}
