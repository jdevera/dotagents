package integrations

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageMCPRejectsAmbiguousRequests(t *testing.T) {
	for _, desired := range [][]MCPDesired{
		{{Client: "unsupported", Name: "a", Definition: map[string]any{}}},
		{{Client: "claude", Name: "--replace", Remove: true}},
		{{Client: "claude", Name: "a"}},
		{{Client: "claude", Name: "a", Remove: true}, {Client: "claude", Name: "a", Remove: true}},
	} {
		if _, err := StageMCP(context.Background(), t.TempDir(), desired); err == nil {
			t.Fatalf("accepted invalid request: %+v", desired)
		}
	}
}

func TestNativeDefinitionEquivalenceOnlyAllowsKnownDefaults(t *testing.T) {
	implicit := map[string]any{"url": "https://example.test"}
	explicit := map[string]any{"type": "http", "url": "https://example.test"}
	if !equivalentDefinition("claude", implicit, explicit) {
		t.Fatal("known Claude transport default rejected")
	}
	if equivalentDefinition("codex", implicit, explicit) {
		t.Fatal("invented a Codex transport default")
	}
	if equivalentDefinition("claude", implicit, map[string]any{"type": "http", "url": "https://example.test", "required": true}) {
		t.Fatal("lost optional settings treated as equivalent")
	}
}

func TestStageMCPRealSkillsharePreservesUnselectedSettings(t *testing.T) {
	if os.Getenv("DOTAGENTS_TEST_SKILLSHARE") != "1" {
		t.Skip("set DOTAGENTS_TEST_SKILLSHARE=1 for isolated real Skillshare publication")
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	claude := []byte(`{"theme":"dark","mcpServers":{"keep":{"type":"http","url":"https://keep.example.test","headers":{"Authorization":"private-untouched"}},"docs":{"type":"http","url":"https://old.example.test"},"retired":{"type":"http","url":"https://retired.example.test"}}}`)
	codex := []byte("# Preserve this comment\nmodel = 'example-model'\n[mcp_servers.keep]\nurl = 'https://keep.example.test'\n[mcp_servers.docs]\nurl = 'https://old.example.test'\n")
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), claude, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex/config.toml"), codex, 0600); err != nil {
		t.Fatal(err)
	}
	stage, err := StageMCP(context.Background(), home, []MCPDesired{
		{Client: "claude", Name: "docs", Definition: map[string]any{"type": "http", "url": "https://claude.example.test"}},
		{Client: "claude", Name: "added", Definition: map[string]any{"type": "http", "url": "https://added.example.test"}},
		{Client: "claude", Name: "retired", Remove: true},
		{Client: "codex", Name: "docs", Definition: map[string]any{"url": "https://codex.example.test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stage.Files) != 2 || !bytes.Equal(stage.Before[".claude.json"], claude) || !bytes.Equal(stage.Before[".codex/config.toml"], codex) {
		t.Fatal("staging did not capture exact before state")
	}
	for relative, expected := range map[string][]byte{".claude.json": claude, ".codex/config.toml": codex} {
		actual, err := os.ReadFile(filepath.Join(home, relative))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("staging changed source %s", relative)
		}
	}
	if !bytes.Contains(stage.Files[".codex/config.toml"], []byte("# Preserve this comment")) {
		t.Fatal("publisher lost original comment")
	}
	if !bytes.Contains(stage.Files[".claude.json"], []byte("private-untouched")) {
		t.Fatal("unselected credential was altered")
	}
	for relative, required := range map[string]string{".claude.json": "https://claude.example.test", ".codex/config.toml": "https://codex.example.test"} {
		if !strings.Contains(string(stage.Files[relative]), required) {
			t.Fatalf("client-specific definition lost in %s", relative)
		}
	}
}

func TestStageMCPRealSkillshareRefusesLossyFields(t *testing.T) {
	if os.Getenv("DOTAGENTS_TEST_SKILLSHARE") != "1" {
		t.Skip("set DOTAGENTS_TEST_SKILLSHARE=1 for isolated real Skillshare publication")
	}
	home := t.TempDir()
	_, err := StageMCP(context.Background(), home, []MCPDesired{{Client: "claude", Name: "custom", Definition: map[string]any{"url": "https://example.test", "future_setting": "preserve-me"}}})
	if err == nil {
		t.Fatal("publisher silently dropped unsupported native setting")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(statErr) {
		t.Fatal("failed staging created a live configuration")
	}
}

func TestProjectMCPRealSkillshareUsesOnlyProjectFiles(t *testing.T) {
	if os.Getenv("DOTAGENTS_TEST_SKILLSHARE") != "1" {
		t.Skip("set DOTAGENTS_TEST_SKILLSHARE=1 for real isolated project MCP publication")
	}
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	projectClaude := []byte(`{"mcpServers":{"keep":{"url":"https://keep.test"},"docs":{"type":"http","url":"https://old.test"}}}`)
	projectCodex := []byte("# Keep project comment\nmodel = 'project-model'\n[mcp_servers.docs]\nurl = 'https://old.test'\n")
	decoy := []byte(`{"global-shaped":"do not read or change"}`)
	for name, data := range map[string][]byte{".mcp.json": projectClaude, ".codex/config.toml": projectCodex, ".claude.json": decoy} {
		if err := os.WriteFile(filepath.Join(project, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := StageMCPProject(context.Background(), project, []MCPDesired{
		{Client: "claude", Name: "docs", Definition: map[string]any{"type": "http", "url": "https://new-claude.test"}},
		{Client: "codex", Name: "docs", Definition: map[string]any{"url": "https://new-codex.test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 2 || result.Files[".mcp.json"] == nil || result.Files[".codex/config.toml"] == nil || result.Files[".claude.json"] != nil {
		t.Fatalf("wrong project publication paths: %v", result.Files)
	}
	for name, expected := range map[string][]byte{".mcp.json": projectClaude, ".codex/config.toml": projectCodex, ".claude.json": decoy} {
		actual, err := os.ReadFile(filepath.Join(project, name))
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatalf("project staging changed source %s", name)
		}
	}
	if !bytes.Contains(result.Files[".mcp.json"], []byte("https://keep.test")) || !bytes.Contains(result.Files[".codex/config.toml"], []byte("# Keep project comment")) {
		t.Fatal("project publisher lost unknown MCP or unrelated configuration comment")
	}
}

func TestProjectMCPRejectsSymlinkToGlobalSettings(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "config.toml"), []byte("secret = 'untouched'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, ".codex")); err != nil {
		t.Fatal(err)
	}
	_, err := StageMCPProject(context.Background(), project, []MCPDesired{{Client: "codex", Name: "docs", Definition: map[string]any{"url": "https://example.test"}}})
	if err == nil {
		t.Fatal("project staging followed a link outside project scope")
	}
}
