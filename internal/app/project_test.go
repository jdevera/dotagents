package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func projectPreviewFixture(t *testing.T) (string, string, nativeProjectPreview) {
	t.Helper()
	directory := t.TempDir()
	source := t.TempDir()
	applyPut(t, filepath.Join(source, "example", "SKILL.md"), "native current content\n")
	data, err := json.Marshal(map[string]any{
		"project": map[string]any{"groups": []any{map[string]string{"target": "project@codex", "skillsPath": filepath.Join(directory, ".agents", "skills")}}},
		"sync":    map[string]any{"results": []any{map[string]any{"target": "project@codex", "linked": []string{"example"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var preview nativeProjectPreview
	if err := json.Unmarshal(data, &preview); err != nil {
		t.Fatal(err)
	}
	return directory, source, preview
}

func TestNativeProjectPreviewProtectsUnknownSkills(t *testing.T) {
	for _, kind := range []string{"directory", "foreign-link", "escaping-parent"} {
		t.Run(kind, func(t *testing.T) {
			directory, source, preview := projectPreviewFixture(t)
			target := filepath.Join(directory, ".agents", "skills", "example")
			switch kind {
			case "directory":
				applyPut(t, filepath.Join(target, "SKILL.md"), "unknown\n")
			case "foreign-link":
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), target); err != nil {
					t.Fatal(err)
				}
			case "escaping-parent":
				if err := os.Symlink(t.TempDir(), filepath.Join(directory, ".agents")); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkNativeProjectPreview(directory, source, []Skill{{Name: "example"}}, preview); err == nil {
				t.Fatalf("unsafe %s accepted", kind)
			}
		})
	}
}

func TestNativeProjectPreviewAllowsSourceEditsButNotPruning(t *testing.T) {
	directory, source, preview := projectPreviewFixture(t)
	preview.Sync.PathOverlap = 2 // Native counts unrelated global discovery too.
	target := filepath.Join(directory, ".agents", "skills", "example")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(source, "example"), target); err != nil {
		t.Fatal(err)
	}
	if err := checkNativeProjectPreview(directory, source, []Skill{{Name: "example"}}, preview); err != nil {
		t.Fatal(err)
	}
	preview.Sync.Results[0].Pruned = []string{"unknown"}
	if err := checkNativeProjectPreview(directory, source, []Skill{{Name: "example"}}, preview); err == nil {
		t.Fatal("pruning allowed during additive topic registration")
	}
}

func TestNativeProjectPreviewRejectsActualTargetOverlap(t *testing.T) {
	directory, source, preview := projectPreviewFixture(t)
	preview.Project.Groups = append(preview.Project.Groups, preview.Project.Groups[0])
	if err := checkNativeProjectPreview(directory, source, []Skill{{Name: "example"}}, preview); err == nil {
		t.Fatal("duplicate physical project target accepted")
	}
	preview.Project.Groups = preview.Project.Groups[:1]
	preview.Project.Groups[0].Path = filepath.Join(directory, ".agents", "skills")
	if err := os.MkdirAll(preview.Project.Groups[0].Path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := checkNativeProjectPreview(directory, preview.Project.Groups[0].Path, []Skill{{Name: "example"}}, preview); err == nil {
		t.Fatal("project target overlapping native source accepted")
	}
}

func TestNativeProjectPreviewRejectsExcludedSelectionAndOtherOverwrite(t *testing.T) {
	directory, source, preview := projectPreviewFixture(t)
	preview.Sync.Results[0].Linked = nil
	if err := checkNativeProjectPreview(directory, source, []Skill{{Name: "example"}}, preview); err == nil {
		t.Fatal("missing selected skill treated as success")
	}
	preview.Sync.Results[0].Linked = []string{"example"}
	preview.Sync.Results[0].Updated = []string{"other"}
	applyPut(t, filepath.Join(directory, ".agents", "skills", "other", "SKILL.md"), "unknown content\n")
	if err := checkNativeProjectPreview(directory, source, []Skill{{Name: "example"}}, preview); err == nil {
		t.Fatal("unselected overwrite allowed")
	}
}
