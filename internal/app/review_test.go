package app

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func reviewTopic(t *testing.T, a *App, id, topic string) {
	t.Helper()
	if _, err := exec.LookPath("skillshare"); err != nil {
		t.Skip("native Skillshare integration requires the skillshare executable")
	}
	addAccepted(t, a, id, "skillshare", ".agents/skills/"+id, "accepted "+id+"\n")
	a.Config.Skills[len(a.Config.Skills)-1].Topics = []string{topic}
	a.Config.Skills[len(a.Config.Skills)-1].Scope = "project"
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(a.Repo, "native-library")
	config, err := json.Marshal(map[string]any{"sources": map[string]string{"skills": library}, "mode": "merge", "targets": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	applyPut(t, a.SkillshareConfigPath(), string(config))
	tree, err := a.Store().Load(a.Config.Skills[len(a.Config.Skills)-1].Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTree(filepath.Join(library, id), tree); err != nil {
		t.Fatal(err)
	}
}

func TestReviewProjectKeepsUntrackedCollision(t *testing.T) {
	a := applyFixture(t)
	reviewTopic(t, a, "example", "frontend")
	project := t.TempDir()
	target := filepath.Join(project, ".agents/skills/example/SKILL.md")
	applyPut(t, target, "untracked project instructions\n")
	if _, err := a.Project(context.Background(), "frontend", project, []string{"codex"}); err == nil {
		t.Fatal("untracked collision was not refused")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "untracked project instructions\n" {
		t.Fatalf("project install overwrote unknown skill: %q %v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(project, ".dotagents-project")); !os.IsNotExist(err) {
		t.Fatal("native project flow created a duplicate dotagents catalog")
	}
}

func TestReviewProjectKeepsEditsInOtherTopics(t *testing.T) {
	a := applyFixture(t)
	reviewTopic(t, a, "one", "first")
	reviewTopic(t, a, "two", "second")
	project := t.TempDir()
	if _, err := a.Project(context.Background(), "first", project, []string{"codex"}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(project, ".agents/skills/one/SKILL.md")
	applyPut(t, target, "local edit to first topic\n")
	if _, err := a.Project(context.Background(), "second", project, []string{"codex"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "local edit to first topic\n" {
		t.Fatalf("installing second topic reset first topic: %q %v", data, err)
	}
}

func TestReviewBackupContainsActiveSkillshareLibrary(t *testing.T) {
	a := applyFixture(t)
	library := filepath.Join(a.Repo, ".dotagents/transactions/test/library/example")
	applyPut(t, filepath.Join(library, "SKILL.md"), "active library contents\n")
	target := filepath.Join(a.Home, ".agents/skills/example")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(library, target); err != nil {
		t.Fatal(err)
	}
	receipt, err := a.Backup()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(receipt.Archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "repository/.dotagents/transactions/test/library/example/SKILL.md" {
			data, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			found = string(data) == "active library contents\n"
		}
	}
	if !found {
		t.Fatal("backup preserves active link but omits the skill contents it points to")
	}
}

func TestReviewProjectEvaluatesHomeConditionAgainstMachineHome(t *testing.T) {
	a := applyFixture(t)
	actualHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "machine-marker")
	applyPut(t, marker, "present\n")
	rel, err := filepath.Rel(actualHome, marker)
	if err != nil {
		t.Fatal(err)
	}
	a.Machine.Home = actualHome
	a.Config.Groups["marked"] = Condition{Exists: []string{"~/" + rel}}
	a.Machine.Groups = []string{"marked"}
	reviewTopic(t, a, "example", "frontend")
	a.Config.Skills[0].Groups = []string{"marked"}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if _, err := a.Project(context.Background(), "frontend", project, []string{"codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, ".agents/skills/example/SKILL.md")); err != nil {
		t.Fatalf("selected home condition silently skipped project installation: %v", err)
	}
}

func TestOnlySelectedSkillIsApplied(t *testing.T) {
	a := applyFixture(t)
	for _, id := range []string{"one", "two"} {
		addAccepted(t, a, id, "manual", ".agents/skills/"+id, "accepted\n")
		applyPut(t, filepath.Join(a.Home, ".agents/skills", id, "SKILL.md"), "local\n")
	}
	a.Only = []string{"two"}
	p := requirePlan(t, a)
	if len(p.Actions) != 1 || p.Actions[0].ID != "two" {
		t.Fatalf("selection ignored: %+v", p)
	}
	if _, err := a.Apply(context.Background(), p.Revision); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(a.Home, ".agents/skills/one/SKILL.md")); string(data) != "local\n" {
		t.Fatal("unselected skill changed")
	}
	a.Only = nil
	if _, err := a.Apply(context.Background(), p.Revision); err == nil {
		t.Fatal("selected revision authorized unselected changes")
	}
}

func TestReviewProjectRejectsRedirectedCatalog(t *testing.T) {
	a := applyFixture(t)
	reviewTopic(t, a, "example", "frontend")
	project := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(project, ".dotagents-project")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Project(context.Background(), "frontend", project, []string{"codex"}); err == nil {
		t.Fatal("project followed redirected catalog")
	}
	if _, err := os.Lstat(filepath.Join(elsewhere, "dotagents.json")); !os.IsNotExist(err) {
		t.Fatal("project wrote outside its catalog directory")
	}
}

func TestReviewProjectTargetsRemainTrackedWhenAddingAgain(t *testing.T) {
	a := applyFixture(t)
	reviewTopic(t, a, "example", "frontend")
	directory := t.TempDir()
	if _, err := a.Project(context.Background(), "frontend", directory, []string{"codex", "claude"}); err != nil {
		t.Fatal(err)
	}
	result, err := a.Project(context.Background(), "frontend", directory, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Config == "" {
		t.Fatal("native registration path missing")
	}
	data, err := a.SkillshareCommand(context.Background(), "project", "show", result.Directory, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var registered struct {
		Project nativeProject `json:"project"`
	}
	if err := json.Unmarshal(data, &registered); err != nil {
		t.Fatal(err)
	}
	if len(registered.Project.Targets) != 2 {
		t.Fatalf("existing native target ownership was dropped: %s", data)
	}
	if len(result.Backups) < 3 {
		t.Fatalf("registration and native target backup results missing: %+v", result)
	}
}

func TestReviewProjectPreservesNativeSourceEditsWithoutAccepting(t *testing.T) {
	a := applyFixture(t)
	reviewTopic(t, a, "example", "frontend")
	directory := t.TempDir()
	if _, err := a.Project(context.Background(), "frontend", directory, []string{"codex"}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, ".agents/skills/example/SKILL.md")
	before := a.Config.Skills[0].Snapshot
	applyPut(t, target, "local project changes\n")
	if _, err := a.Project(context.Background(), "frontend", directory, []string{"codex"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "local project changes\n" {
		t.Fatal("project update destroyed local edits")
	}
	if a.Config.Skills[0].Snapshot != before {
		t.Fatal("native source edits were automatically accepted into audit catalog")
	}
}
