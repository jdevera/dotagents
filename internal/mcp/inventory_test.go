package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, home, relative, content string) {
	t.Helper()
	path := filepath.Join(home, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestScanRedactsValuesButDetectsCredentialChanges(t *testing.T) {
	home := t.TempDir()
	const secret = "must-not-appear-in-output"
	writeFixture(t, home, ".claude.json", `{"mcpServers":{"a":{"command":"`+secret+`","args":["`+secret+`"],"env":{"TOKEN":"`+secret+`"}}},"projects":{"/project":{"mcpServers":{"not-global":{}}}}}`)
	writeFixture(t, home, ".codex/config.toml", "[mcp_servers.\"quoted.name\"]\nurl = 'https://user:"+secret+"@example.test/token?key="+secret+"'\nenabled = false\n[mcp_servers.\"quoted.name\".http_headers]\nAuthorization = 'Bearer "+secret+"'\n")
	first := Scan(home)
	if len(first.Issues) != 0 || len(first.Entries) != 2 {
		t.Fatalf("inventory: %+v", first)
	}
	data, err := json.Marshal(first)
	if err != nil || strings.Contains(string(data), secret) {
		t.Fatalf("report exposed a secret or failed encoding: %s, %v", data, err)
	}
	if first.Entries[1].Name != "quoted.name" || first.Entries[1].Enabled {
		t.Fatalf("quoted server name or enabled state lost: %+v", first.Entries[1])
	}
	writeFixture(t, home, ".claude.json", `{"mcpServers":{"a":{"command":"different"}}}`)
	second := Scan(home)
	if first.Entries[0].Fingerprint == second.Entries[0].Fingerprint {
		t.Fatal("credential/config mutation not detected")
	}
}

func TestScanEquivalentReformattingAndReadOnly(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, home, ".claude.json", `{"mcpServers":{"a":{"url":"https://example.test","env":{"B":"2","A":"1"}}}}`)
	first := Scan(home)
	text := "{\n\"mcpServers\":{\"a\":{\"env\":{\"A\":\"1\",\"B\":\"2\"},\"url\":\"https://example.test\"}}}"
	writeFixture(t, home, ".claude.json", text)
	second := Scan(home)
	if len(first.Entries) != 1 || len(second.Entries) != 1 || first.Entries[0].Fingerprint != second.Entries[0].Fingerprint {
		t.Fatalf("reformat changed fingerprint: %+v, %+v", first, second)
	}
	actual, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	if string(actual) != text {
		t.Fatal("scan rewrote configuration")
	}
}

func TestScanReportsInvalidSourcesWithoutLeakingParseContext(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, home, ".claude.json", `{"mcpServers": "private-token"}`)
	writeFixture(t, home, ".codex/config.toml", "[mcp_servers.a]\nurl = private-token\n")
	result := Scan(home)
	if len(result.Issues) != 2 || len(result.Entries) != 0 {
		t.Fatalf("invalid sources silently ignored: %+v", result)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "private-token") {
		t.Fatal("parse error exposed source value")
	}
}

func TestProjectScanAndReadUseOnlyProjectPaths(t *testing.T) {
	project := t.TempDir()
	writeFixture(t, project, ".mcp.json", `{"mcpServers":{"project-only":{"type":"http","url":"https://project.test"}}}`)
	writeFixture(t, project, ".claude.json", `{"mcpServers":{"global-shaped-decoy":{"url":"https://not-project.test"}}}`)
	writeFixture(t, project, ".codex/config.toml", "[mcp_servers.project-codex]\nurl = 'https://project-codex.test'\n")
	result := ScanProject(project)
	if len(result.Issues) != 0 || len(result.Entries) != 2 || result.Entries[0].Name != "project-only" || result.Entries[0].Path != filepath.Join(project, ".mcp.json") {
		t.Fatalf("incorrect project scope: %+v", result)
	}
	definition, err := ReadProjectDefinition(project, "claude", "project-only")
	if err != nil || definition["url"] != "https://project.test" {
		t.Fatal("project definition could not be read", err)
	}
	if _, err := ReadProjectDefinition(project, "claude", "global-shaped-decoy"); err == nil {
		t.Fatal("project reader consumed global-shaped config")
	}
}
