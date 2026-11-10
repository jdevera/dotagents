package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdevera/dotagents/internal/integrations"
	"github.com/jdevera/dotagents/internal/mcp"
	"github.com/pelletier/go-toml/v2"
)

func acceptedMCP(t *testing.T, a *App, client, name string, definition map[string]any, groups []string) {
	t.Helper()
	hash, err := mcp.Fingerprint(definition)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(definition)
	if err := atomicWrite(filepath.Join(a.Repo, ".dotagents/private/mcp", hash+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	a.Config.MCP = append(a.Config.MCP, MCPRule{Client: client, Name: name, Fingerprint: hash, Groups: groups, Definition: mcp.Redact(definition)})
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
}

func fakeMCPStage(_ context.Context, home string, desired []integrations.MCPDesired) (integrations.MCPStage, error) {
	result := integrations.MCPStage{Files: map[string][]byte{}, Before: map[string][]byte{}}
	documents := map[string]map[string]any{}
	for _, item := range desired {
		relative, field, err := mcpNativePath(item.Client, "")
		if err != nil {
			return result, err
		}
		if _, exists := documents[item.Client]; !exists {
			before, err := os.ReadFile(filepath.Join(home, relative))
			if err != nil && !os.IsNotExist(err) {
				return result, err
			}
			result.Before[relative] = before
			document, err := decodeMCPFile(item.Client, before)
			if err != nil {
				return result, err
			}
			documents[item.Client] = document
		}
		servers, _ := documents[item.Client][field].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
			documents[item.Client][field] = servers
		}
		if item.Remove {
			delete(servers, item.Name)
		} else {
			servers[item.Name] = item.Definition
		}
	}
	for client, document := range documents {
		relative, _, _ := mcpNativePath(client, "")
		var data []byte
		var err error
		if client == "codex" {
			data, err = toml.Marshal(document)
		} else {
			data, err = json.MarshalIndent(document, "", "  ")
		}
		if err != nil {
			return result, err
		}
		result.Files[relative] = data
	}
	return result, nil
}

func mcpPrepared(t *testing.T, a *App) preparedMCP {
	t.Helper()
	p, err := a.prepareMCP(context.Background(), fakeMCPStage)
	if err != nil || len(p.Plan.Issues) != 0 {
		t.Fatalf("MCP preparation failed: %+v %v", p.Plan, err)
	}
	return p
}

func TestMCPApplyAndRollbackPreserveCompleteNativeFile(t *testing.T) {
	a := applyFixture(t)
	original := `{"theme":"dark","mcpServers":{"docs":{"url":"https://old.example.test","type":"http"},"unknown":{"command":"keep-private-secret"}}}` + "\n"
	path := filepath.Join(a.Home, ".claude.json")
	applyPut(t, path, original)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	acceptedMCP(t, a, "claude", "docs", map[string]any{"url": "https://accepted.example.test", "type": "http"}, nil)
	p := mcpPrepared(t, a)
	if len(p.Plan.Actions) != 1 || p.Plan.Actions[0].Kind != "update" {
		t.Fatalf("incorrect plan: %+v", p.Plan)
	}
	public, _ := json.Marshal(p.Plan)
	if strings.Contains(string(public), "keep-private-secret") || strings.Contains(string(public), "accepted.example.test") {
		t.Fatal("public plan exposed private native values")
	}
	if current, _ := os.ReadFile(path); string(current) != original {
		t.Fatal("planning mutated native settings")
	}
	id, err := a.mcpApply(context.Background(), p.Plan.Revision, fakeMCPStage)
	if err != nil {
		t.Fatal(err)
	}
	applied, _ := os.ReadFile(path)
	if !bytes.Contains(applied, []byte("accepted.example.test")) || !bytes.Contains(applied, []byte("keep-private-secret")) {
		t.Fatal("apply lost accepted or unknown configuration")
	}
	private := filepath.Join(a.Repo, ".dotagents/mcp-transactions", id)
	for _, name := range []string{"before-0000", "after-0000", "transaction.json"} {
		info, err := os.Stat(filepath.Join(private, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private backup permissions %s: %v", name, err)
		}
	}
	if err := a.MCPRollback(id); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(restored) != original || info.Mode().Perm() != 0600 {
		t.Fatal("rollback did not restore exact original bytes and permissions")
	}
	if err := a.MCPRollback(id); err != nil {
		t.Fatal("repeated rollback is not idempotent", err)
	}
}

func TestMCPInstallRollbackRemovesPreviouslyAbsentFile(t *testing.T) {
	a := applyFixture(t)
	acceptedMCP(t, a, "codex", "docs", map[string]any{"url": "https://example.test"}, nil)
	p := mcpPrepared(t, a)
	id, err := a.mcpApply(context.Background(), p.Plan.Revision, fakeMCPStage)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.MCPRollback(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.Home, ".codex/config.toml")); !os.IsNotExist(err) {
		t.Fatal("rollback left a previously absent configuration behind")
	}
}

func TestMCPRejectsStalePlanAndPostApplyEdits(t *testing.T) {
	a := applyFixture(t)
	path := filepath.Join(a.Home, ".claude.json")
	applyPut(t, path, `{"mcpServers":{"docs":{"url":"https://old.test"}}}`)
	acceptedMCP(t, a, "claude", "docs", map[string]any{"url": "https://new.test"}, nil)
	p := mcpPrepared(t, a)
	applyPut(t, path, `{"new-setting":true,"mcpServers":{"docs":{"url":"https://old.test"}}}`)
	if _, err := a.mcpApply(context.Background(), p.Plan.Revision, fakeMCPStage); err == nil {
		t.Fatal("stale plan was applied")
	}
	p = mcpPrepared(t, a)
	id, err := a.mcpApply(context.Background(), p.Plan.Revision, fakeMCPStage)
	if err != nil {
		t.Fatal(err)
	}
	const edited = `{"user-edit":"preserve-this"}`
	applyPut(t, path, edited)
	if err := a.MCPRollback(id); err == nil {
		t.Fatal("rollback clobbered post-apply edits")
	}
	current, _ := os.ReadFile(path)
	if string(current) != edited {
		t.Fatal("rollback modified edited configuration")
	}
}

func TestMCPRollbackRefusesDamagedBackup(t *testing.T) {
	a := applyFixture(t)
	applyPut(t, filepath.Join(a.Home, ".claude.json"), `{"mcpServers":{"docs":{"url":"https://old.test"}}}`)
	acceptedMCP(t, a, "claude", "docs", map[string]any{"url": "https://new.test"}, nil)
	p := mcpPrepared(t, a)
	id, err := a.mcpApply(context.Background(), p.Plan.Revision, fakeMCPStage)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(a.Repo, ".dotagents/mcp-transactions", id, "before-0000")
	if err := os.WriteFile(backup, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.MCPRollback(id); err == nil {
		t.Fatal("corrupt backup accepted")
	}
}

func TestMCPGroupsRemoveOnlyExplicitManagedEntries(t *testing.T) {
	a := applyFixture(t)
	a.Config.Groups["elsewhere"] = Condition{Hostnames: []string{"a-hostname-that-does-not-exist"}}
	applyPut(t, filepath.Join(a.Home, ".claude.json"), `{"mcpServers":{"managed":{"url":"https://managed.test"},"unknown":{"url":"https://keep.test"}}}`)
	acceptedMCP(t, a, "claude", "managed", map[string]any{"url": "https://managed.test"}, []string{"elsewhere"})
	p := mcpPrepared(t, a)
	if len(p.Plan.Actions) != 1 || p.Plan.Actions[0].Kind != "remove" || p.Plan.Actions[0].Name != "managed" {
		t.Fatalf("wrong deselection plan: %+v", p.Plan)
	}
	if _, err := a.mcpApply(context.Background(), p.Plan.Revision, fakeMCPStage); err != nil {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(filepath.Join(a.Home, ".claude.json"))
	if bytes.Contains(current, []byte("managed.test")) || !bytes.Contains(current, []byte("keep.test")) {
		t.Fatal("deselection removed unknown configuration or retained selected removal")
	}
}

func TestMCPMissingPrivateSnapshotAndSymlinksBlockPublication(t *testing.T) {
	a := applyFixture(t)
	acceptedMCP(t, a, "claude", "docs", map[string]any{"url": "https://new.test"}, nil)
	private := filepath.Join(a.Repo, ".dotagents/private/mcp", a.Config.MCP[0].Fingerprint+".json")
	if err := os.Remove(private); err != nil {
		t.Fatal(err)
	}
	p, err := a.prepareMCP(context.Background(), fakeMCPStage)
	if err != nil || len(p.Plan.Issues) != 1 || !strings.Contains(p.Plan.Issues[0], "private snapshot") {
		t.Fatalf("missing raw snapshot not reported: %+v %v", p.Plan, err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	applyPut(t, outside, `{"private":"untouched"}`)
	if err := os.Symlink(outside, filepath.Join(a.Home, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	p, err = a.prepareMCP(context.Background(), fakeMCPStage)
	if err != nil || len(p.Plan.Issues) == 0 || !strings.Contains(p.Plan.Issues[0], "link") {
		t.Fatalf("linked native config not refused: %+v %v", p.Plan, err)
	}
	actual, _ := os.ReadFile(outside)
	if string(actual) != `{"private":"untouched"}` {
		t.Fatal("outside configuration changed")
	}
}

func TestMCPKnownClaudeDefaultsDoNotProducePerpetualPlan(t *testing.T) {
	a := applyFixture(t)
	applyPut(t, filepath.Join(a.Home, ".claude.json"), `{"mcpServers":{"docs":{"type":"http","url":"https://same.test"}}}`)
	acceptedMCP(t, a, "claude", "docs", map[string]any{"url": "https://same.test"}, nil)
	p := mcpPrepared(t, a)
	if len(p.Plan.Actions) != 0 {
		t.Fatalf("equivalent publisher default caused perpetual drift: %+v", p.Plan)
	}
}

func TestMCPCompensationAndInterruptedRollback(t *testing.T) {
	a := applyFixture(t)
	path := filepath.Join(a.Home, ".claude.json")
	const original = `{"mcpServers":{"docs":{"url":"https://original.test"}}}`
	const replacement = `{"mcpServers":{"docs":{"url":"https://replacement.test"}}}`
	applyPut(t, path, original)
	state, err := inspectMCPFile(a.Home, ".claude.json", a.Config.Scope)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := newTransactionID()
	dir := filepath.Join(a.Repo, ".dotagents/mcp-transactions", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	entry := mcpTransactionEntry{Relative: ".claude.json", Existed: true, BeforeHash: state.Hash, BeforeMode: state.Mode, AfterHash: hashBytes([]byte(replacement)), AfterMode: state.Mode}
	if err := privateMCPFile(dir, "before-0000", []byte(original)); err != nil {
		t.Fatal(err)
	}
	if err := privateMCPFile(dir, "after-0000", []byte(replacement)); err != nil {
		t.Fatal(err)
	}
	record := mcpTransaction{Version: 1, ID: id, Home: a.Home, Repo: a.Repo, Status: "applying", Entries: []mcpTransactionEntry{entry}}
	if err := writeMCPTransaction(dir, record); err != nil {
		t.Fatal(err)
	}
	if err := a.publishMCPFile(dir, id, 0, entry); err != nil {
		t.Fatal(err)
	}
	// Simulate a later file failing: recovery compensates the already published
	// file without invoking any installer again.
	if err := a.rollbackMCP(dir, &record); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != original {
		t.Fatal("partial publication was not compensated")
	}
	// Simulate an interruption between capturing the applied file during
	// rollback and relinking the original from the verified private backup.
	applyPut(t, path, replacement)
	old, _ := mcpTempPaths(a.Home, id, 0, entry.Relative)
	if err := os.Rename(path, old+"-applied"); err != nil {
		t.Fatal(err)
	}
	record.Status = "rolling_back"
	if err := writeMCPTransaction(dir, record); err != nil {
		t.Fatal(err)
	}
	if err := a.MCPRollback(id); err != nil {
		t.Fatal(err)
	}
	actual, _ = os.ReadFile(path)
	if string(actual) != original {
		t.Fatal("interrupted rollback did not recover exact original")
	}
}

func TestMCPRealPublisherTransaction(t *testing.T) {
	if os.Getenv("DOTAGENTS_TEST_SKILLSHARE") != "1" {
		t.Skip("set DOTAGENTS_TEST_SKILLSHARE=1 to exercise the real isolated publisher")
	}
	a := applyFixture(t)
	path := filepath.Join(a.Home, ".claude.json")
	const original = `{"mcpServers":{"docs":{"type":"http","url":"https://old.test"},"keep":{"command":"unknown-command"}}}`
	applyPut(t, path, original)
	acceptedMCP(t, a, "claude", "docs", map[string]any{"type": "http", "url": "https://new.test"}, nil)
	p, err := a.MCPPlan(context.Background())
	if err != nil || len(p.Issues) > 0 {
		t.Fatalf("real MCP plan failed: %+v %v", p, err)
	}
	id, err := a.MCPApply(context.Background(), p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.MCPRollback(id); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != original {
		t.Fatal("real publisher transaction did not roll back exact original")
	}
}

func TestProjectMCPRealTransactionAndScopeBoundRollback(t *testing.T) {
	if os.Getenv("DOTAGENTS_TEST_SKILLSHARE") != "1" {
		t.Skip("set DOTAGENTS_TEST_SKILLSHARE=1 for real isolated project MCP transaction")
	}
	a := applyFixture(t)
	a.Config.Scope = "project"
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(a.Home, ".mcp.json")
	const original = `{"mcpServers":{"docs":{"type":"http","url":"https://old-project.test"},"unknown":{"command":"preserve-unknown"}}}`
	const decoy = `{"global-shaped":"never touch"}`
	applyPut(t, projectPath, original)
	applyPut(t, filepath.Join(a.Home, ".claude.json"), decoy)
	acceptedMCP(t, a, "claude", "docs", map[string]any{"type": "http", "url": "https://new-project.test"}, nil)
	p, err := a.MCPPlan(context.Background())
	if err != nil || len(p.Issues) != 0 || len(p.Actions) != 1 || p.Actions[0].Path != projectPath {
		t.Fatalf("project MCP plan failed: %+v %v", p, err)
	}
	id, err := a.MCPApply(context.Background(), p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(projectPath)
	if !bytes.Contains(actual, []byte("new-project.test")) || !bytes.Contains(actual, []byte("preserve-unknown")) {
		t.Fatal("project MCP transaction lost desired or unknown settings")
	}
	globalShaped, _ := os.ReadFile(filepath.Join(a.Home, ".claude.json"))
	if string(globalShaped) != decoy {
		t.Fatal("project transaction changed global-shaped settings")
	}
	a.Config.Scope = ""
	if err := a.MCPRollback(id); err == nil {
		t.Fatal("project transaction could be rolled back under global scope")
	}
	a.Config.Scope = "project"
	if err := a.MCPRollback(id); err != nil {
		t.Fatal(err)
	}
	actual, _ = os.ReadFile(projectPath)
	if string(actual) != original {
		t.Fatal("project rollback did not restore exact original bytes")
	}
}

func TestPortableMCPTemplateInstallsWithoutPrivateSnapshotOnMatchingMachine(t *testing.T) {
	a := applyFixture(t)
	a.Config.Scope = "home"
	a.Config.Groups["this-machine"] = Condition{Hostnames: []string{a.Machine.Hostname}}
	a.Config.Groups["other-machines"] = Condition{Hostnames: []string{"another-host-that-does-not-exist"}}
	a.Config.MCP = []MCPRule{
		{Client: "claude", Name: "portable", Groups: []string{"this-machine"}, Template: map[string]any{"type": "http", "url": "https://portable.example.test"}},
		{Client: "claude", Name: "elsewhere", Groups: []string{"other-machines"}, Template: map[string]any{"type": "http", "url": "https://elsewhere.example.test"}},
	}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	var err error
	a, err = a.Reload()
	if err != nil {
		t.Fatal(err)
	}
	p := mcpPrepared(t, a)
	if len(p.Plan.Actions) != 1 || p.Plan.Actions[0].Name != "portable" || p.Plan.Actions[0].Kind != "install" {
		t.Fatalf("portable template did not honor machine groups: %+v", p.Plan)
	}
	if _, err := os.Stat(filepath.Join(a.Repo, ".dotagents/private/mcp")); !os.IsNotExist(err) {
		t.Fatal("template unexpectedly needs a private accepted snapshot")
	}
	id, err := a.mcpApply(context.Background(), p.Plan.Revision, fakeMCPStage)
	if err != nil {
		t.Fatal(err)
	}
	installed, _ := os.ReadFile(filepath.Join(a.Home, ".claude.json"))
	if !bytes.Contains(installed, []byte("portable.example.test")) || bytes.Contains(installed, []byte("elsewhere.example.test")) {
		t.Fatal("portable template installed into the wrong machine group")
	}
	if err := a.MCPRollback(id); err != nil {
		t.Fatal(err)
	}
}
