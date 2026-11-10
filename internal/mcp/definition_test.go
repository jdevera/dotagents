package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefinitionReadIsExplicitAndPublicViewHidesSecrets(t *testing.T) {
	home := t.TempDir()
	const secret = "private-sentinel-token"
	writeFixture(t, home, ".claude.json", `{"mcpServers":{"test":{"type":"stdio","command":"`+secret+`","args":["--token","`+secret+`","--key=`+secret+`"],"url":"https://user:`+secret+`@example.test/`+secret+`?key=`+secret+`","env":{"TOKEN":"`+secret+`","REFERENCE":"${TOKEN}"},"headers":{"Authorization":"`+secret+`"},"unknown":{"innocent":"`+secret+`"},"enabled":true,"startup_timeout_sec":15}}}`)
	definition, err := ReadDefinition(home, "claude", "test")
	if err != nil {
		t.Fatal(err)
	}
	if definition["command"] != secret {
		t.Fatal("private snapshot lost the original definition")
	}
	view := Redact(definition)
	encoded, err := json.Marshal(view)
	if err != nil || strings.Contains(string(encoded), secret) {
		t.Fatalf("public view leaks secret: %s %v", encoded, err)
	}
	if view["type"] != "stdio" || view["enabled"] != true || view["startup_timeout_sec"] != float64(15) {
		t.Fatalf("useful nonsecret settings lost: %s", encoded)
	}
	if !strings.Contains(string(encoded), "${TOKEN}") || !strings.Contains(string(encoded), "--token") {
		t.Fatalf("safe references and flag names lost: %s", encoded)
	}
	if definition["command"] != secret {
		t.Fatal("redaction mutated original definition")
	}
}

func TestSecretOnlyDiffIsVisibleWithoutValues(t *testing.T) {
	before := map[string]any{"env": map[string]any{"TOKEN": "old-private-value"}}
	after := map[string]any{"env": map[string]any{"TOKEN": "new-private-value"}}
	diff := DefinitionDiff(before, after)
	if len(diff) != 1 || !strings.Contains(diff[0].Diff, "hidden") {
		t.Fatalf("secret-only change not reported: %+v", diff)
	}
	encoded, _ := json.Marshal(diff)
	if strings.Contains(string(encoded), "private-value") {
		t.Fatal("secret exposed in diff")
	}
	one, err := Fingerprint(before)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Fingerprint(after)
	if err != nil || one == two {
		t.Fatal("full content fingerprints did not detect secret change", err)
	}
}

func TestStructuralDiffDoesNotExposePrivateValues(t *testing.T) {
	before := map[string]any{"enabled": true, "command": "secret-before"}
	after := map[string]any{"enabled": false, "command": "secret-after"}
	diff := DefinitionDiff(before, after)
	encoded, _ := json.Marshal(diff)
	if len(diff) != 1 || strings.Contains(string(encoded), "secret-before") || strings.Contains(string(encoded), "secret-after") {
		t.Fatalf("unexpected or unsafe diff: %s", encoded)
	}
	if !strings.Contains(diff[0].Diff, "false") || !strings.Contains(diff[0].Diff, "true") {
		t.Fatal("safe setting change not visible")
	}
	if len(DefinitionDiff(before, before)) != 0 {
		t.Fatal("unchanged definition reported changed")
	}
}

func TestReadDefinitionSupportsQuotedTomlNamesAndRejectsMissing(t *testing.T) {
	home := t.TempDir()
	writeFixture(t, home, ".codex/config.toml", "[mcp_servers.\"some.server\"]\ncommand = 'example'\n[mcp_servers.\"some.server\".env]\nTOKEN = 'secret'\n")
	definition, err := ReadDefinition(home, "codex", "some.server")
	if err != nil || definition["command"] != "example" {
		t.Fatal("could not read native definition", err)
	}
	for _, pair := range [][2]string{{"codex", "missing"}, {"other", "some.server"}} {
		if _, err := ReadDefinition(home, pair[0], pair[1]); err == nil {
			t.Fatalf("silently accepted missing/unsupported definition: %v", pair)
		}
	}
}

func TestEquivalentTransportDefaultDoesNotWeakenRawIntegrity(t *testing.T) {
	before := map[string]any{"url": "https://example.test"}
	after := map[string]any{"url": "https://example.test", "type": "http"}
	if !Equivalent("claude", before, after) || len(DefinitionDiffFor("claude", before, after)) != 0 {
		t.Fatal("proven Claude default was reported as drift")
	}
	if Equivalent("codex", before, after) {
		t.Fatal("Claude default incorrectly applied to Codex")
	}
	a, _ := Fingerprint(before)
	b, _ := Fingerprint(after)
	if a == b {
		t.Fatal("raw private snapshot integrity stopped detecting byte-level setting changes")
	}
	after["url"] = "https://changed.example.test"
	if Equivalent("claude", before, after) {
		t.Fatal("actual configuration change was normalized away")
	}
}
