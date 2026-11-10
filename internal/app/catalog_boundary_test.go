package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableMCPTemplateWithoutPrivateCaptureAndHandEdit(t *testing.T) {
	a := applyFixture(t)
	definition := filepath.Join(t.TempDir(), "server.json")
	applyPut(t, definition, `{"url":"https://mcp.example.test/original"}`)
	if err := a.DefineMCP("claude", "example", definition, nil); err != nil {
		t.Fatal(err)
	}
	applyPut(t, filepath.Join(a.Home, ".claude.json"), `{"mcpServers":{"example":{"url":"https://mcp.example.test/original"}}}`)
	if f := finding(t, a.Report(context.Background()), "mcp:claude:example"); f.Status != "ok" {
		t.Fatalf("portable template did not match without private capture: %+v", f)
	}
	if _, err := os.Stat(filepath.Join(a.Repo, ".dotagents/private/mcp")); !os.IsNotExist(err) {
		t.Fatal("portable template unnecessarily created private captured definition")
	}
	oldFingerprint := a.Config.MCP[0].Fingerprint
	a.Config.MCP[0].Template["url"] = "https://mcp.example.test/revised"
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(a.Repo, a.Home)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Config.MCP[0].Fingerprint != oldFingerprint {
		t.Fatal("test must preserve stale stored fingerprint to exercise current-template comparison")
	}
	f := finding(t, fresh.Report(context.Background()), "mcp:claude:example")
	if f.Status != "changed" || len(f.InstalledChanges) == 0 {
		t.Fatalf("hand-edited template was incorrectly reported clean: %+v", f)
	}
	if f.InstalledChanges[0].Diff == "" {
		t.Fatal("template drift has no explanation")
	}
}

func TestPortableMCPRejectsCommonLiteralCredentials(t *testing.T) {
	for _, definition := range []string{
		`{"url":"https://user:password@example.test/mcp"}`,
		`{"url":"https://example.test/mcp?token=secret"}`,
		`{"command":"server","env":{"TOKEN":"literal-secret"}}`,
		`{"url":"https://example.test/mcp","headers":{"Authorization":"Bearer secret"}}`,
		`{"command":"server","args":["--token","literal-secret"]}`,
	} {
		a := applyFixture(t)
		path := filepath.Join(t.TempDir(), "server.json")
		applyPut(t, path, definition)
		if err := a.DefineMCP("claude", "example", path, nil); err == nil {
			t.Fatalf("accepted obvious literal credential: %s", definition)
		}
		data, err := os.ReadFile(filepath.Join(a.Repo, "dotagents.json"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "literal-secret") {
			t.Fatal("rejected credential entered catalog")
		}
	}
}

func TestPortableMCPDoesNotExecuteAuthoredCommand(t *testing.T) {
	a := applyFixture(t)
	marker := filepath.Join(t.TempDir(), "should-not-exist")
	command := filepath.Join(t.TempDir(), "server")
	applyPut(t, command, "#!/bin/sh\ntouch '"+marker+"'\n")
	if err := os.Chmod(command, 0755); err != nil {
		t.Fatal(err)
	}
	definition := filepath.Join(t.TempDir(), "server.json")
	applyPut(t, definition, `{"command":"`+command+`","env":{"TOKEN":"${MCP_TOKEN}"}}`)
	if err := a.DefineMCP("claude", "example", definition, nil); err != nil {
		t.Fatal(err)
	}
	_ = a.Report(context.Background())
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("definition/check executed authored MCP command")
	}
}

func appTDPackage(t *testing.T, root, body string) {
	t.Helper()
	installer, err := os.ReadFile(filepath.Join("..", "providers", "testdata", "td-create-installer.js"))
	if err != nil {
		t.Fatal(err)
	}
	applyPut(t, filepath.Join(root, "package.json"), `{"name":"@doist/todoist-cli","version":"5.12.0","license":"MIT"}`)
	applyPut(t, filepath.Join(root, "dist/lib/skills/create-installer.js"), string(installer))
	applyPut(t, filepath.Join(root, "dist/lib/skills/content.js"), "export const SKILL_NAME = 'todoist-cli';\nexport const SKILL_DESCRIPTION = 'Manage tasks';\nexport const SKILL_COMPATIBILITY = \"Requires td\";\nexport const SKILL_CONTENT = `"+body+"`;\n//# sourceMappingURL=content.js.map")
}

func TestAddToolRecordsActualAcceptedOutputAndProviderPointer(t *testing.T) {
	a := applyFixture(t)
	provider := t.TempDir()
	appTDPackage(t, provider, "# Original tool output\n")
	entry, err := a.AddTool(context.Background(), "td", provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Owner != "td" || entry.ProviderPath != provider || entry.Source != "tool:td@5.12.0" || len(entry.Locations) != 2 {
		t.Fatalf("incomplete tool ownership record: %+v", entry)
	}
	accepted, err := a.Store().Load(entry.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(accepted["SKILL.md"].Data), "Original tool output") {
		t.Fatal("actual tool output was not saved")
	}
	for _, location := range entry.Locations {
		if _, err := os.Stat(filepath.Join(a.Home, location)); !os.IsNotExist(err) {
			t.Fatal("adding tool unexpectedly installed skills")
		}
	}
	appTDPackage(t, provider, "# Updated tool output\n")
	f := finding(t, a.Report(context.Background()), "todoist-cli")
	if len(f.GeneratedChanges) != 1 || !strings.Contains(f.GeneratedChanges[0].Diff, "Updated tool output") {
		t.Fatalf("current tool divergence was not shown: %+v", f)
	}
	if _, err := a.AddTool(context.Background(), "td", provider, nil, nil); err == nil {
		t.Fatal("duplicate tool silently replaced accepted version")
	}
	unchanged, err := a.Store().Load(entry.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unchanged["SKILL.md"].Data), "Original tool output") {
		t.Fatal("probe/duplicate add overwrote accepted output")
	}
}

func TestAddToolUnsupportedAndInvalidTargetsDoNotWriteCatalog(t *testing.T) {
	a := applyFixture(t)
	before, err := os.ReadFile(filepath.Join(a.Repo, "dotagents.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddTool(context.Background(), "tdc", "", nil, nil); err == nil || !strings.Contains(err.Error(), "no safe output adapter") {
		t.Fatalf("unknown tool output claimed: %v", err)
	}
	if _, err := a.AddTool(context.Background(), "td", "/not-a-tool", []string{"invalid-agent"}, nil); err == nil {
		t.Fatal("invalid target accepted")
	}
	after, err := os.ReadFile(filepath.Join(a.Repo, "dotagents.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected tool command changed catalog")
	}
}

func TestAcceptToolUpdatesProvenanceAlongWithSnapshot(t *testing.T) {
	a := applyFixture(t)
	provider := t.TempDir()
	appTDPackage(t, provider, "# Original output\n")
	entry, err := a.AddTool(context.Background(), "td", provider, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	appTDPackage(t, provider, "# Revised output\n")
	applyPut(t, filepath.Join(provider, "package.json"), `{"name":"@doist/todoist-cli","version":"5.13.0","license":"MIT"}`)
	if err := a.Accept(context.Background(), entry.ID, "tool"); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(a.Repo, a.Home)
	if err != nil {
		t.Fatal(err)
	}
	updated := fresh.Config.Skills[0]
	if updated.Source != "tool:td@5.13.0" || updated.Snapshot == entry.Snapshot {
		t.Fatalf("accepted bytes and provenance disagree: %+v", updated)
	}
	old, err := a.Store().Load(entry.Snapshot)
	if err != nil || !strings.Contains(string(old["SKILL.md"].Data), "Original output") {
		t.Fatalf("prior accepted snapshot lost: %v", err)
	}
}

func TestPortableMCPRejectsInvalidDefinitionInputs(t *testing.T) {
	for _, definition := range []string{
		`{"url":"not-a-url"}`,
		`{"url":"file:///private/settings"}`,
		`{"url":"https://mcp.example.test","bearer_token_env_var":"not a variable"}`,
	} {
		a := applyFixture(t)
		path := filepath.Join(t.TempDir(), "server.json")
		applyPut(t, path, definition)
		if err := a.DefineMCP("codex", "example", path, nil); err == nil {
			t.Fatalf("accepted invalid native definition: %s", definition)
		}
	}
	a := applyFixture(t)
	if err := a.DefineMCP("claude", "example", t.TempDir(), nil); err == nil {
		t.Fatal("directory accepted as a definition")
	}
	path := filepath.Join(t.TempDir(), "large.json")
	applyPut(t, path, strings.Repeat(" ", (1<<20)+1))
	if err := a.DefineMCP("claude", "example", path, nil); err == nil {
		t.Fatal("oversized definition accepted")
	}
}

func TestRefreshRejectsWrongOwnerAndUnknownIDBeforeFetching(t *testing.T) {
	a := applyFixture(t)
	addAccepted(t, a, "manual", "manual", ".agents/skills/manual", "accepted\n")
	for _, id := range []string{"manual", "missing"} {
		if _, err := a.Refresh(context.Background(), id, "", false); err == nil {
			t.Fatalf("refresh accepted unsupported item %s", id)
		}
	}
}
