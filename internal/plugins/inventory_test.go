package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOnlyEnabledRegistrySkillsDiscovered(t *testing.T) {
	home := t.TempDir()
	put := func(path, text string) {
		t.Helper()
		path = filepath.Join(home, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(".claude/settings.json", `{"enabledPlugins":{"active@store":true,"disabled@store":false}}`)
	active := filepath.Join(home, ".claude/plugins/cache/store/active/v1")
	put(".claude/plugins/installed_plugins.json", `{"plugins":{"active@store":[{"scope":"user","installPath":"`+filepath.ToSlash(active)+`","version":"1"}],"disabled@store":[{"scope":"user","installPath":"untrusted-disabled"}]}}`)
	put(".claude/plugins/cache/store/active/v1/skills/a/SKILL.md", "active")
	put(".claude/plugins/cache/store/stale/v4/skills/old/SKILL.md", "not active")
	put(".codex/config.toml", "[plugins.\"runtime@store\"]\nenabled = true\n")
	r := Scan(home)
	if len(r.Issues) != 0 {
		t.Fatal(r.Issues)
	}
	if len(r.SkillRoots) != 1 || r.SkillRoots[0] != filepath.Join(active, "skills") {
		t.Fatalf("wrong roots: %#v", r.SkillRoots)
	}
	if len(r.Entries) != 3 {
		t.Fatal(r.Entries)
	}
}
