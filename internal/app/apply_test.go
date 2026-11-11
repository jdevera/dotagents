package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jdevera/dotagents/internal/skills"
)

func applyFixture(t *testing.T) *App {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	a := &App{Repo: repo, Home: home, Config: Config{Version: 1, Groups: map[string]Condition{}}}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	a, err := Open(repo, home)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func applyPut(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func addAccepted(t *testing.T, a *App, id, owner, location, text string) {
	t.Helper()
	snapshot, err := a.Store().Save(skills.Tree{"SKILL.md": {Data: []byte(text), Mode: 0644}, "references/guide.md": {Data: []byte("accepted reference\n"), Mode: 0644}})
	if err != nil {
		t.Fatal(err)
	}
	a.Config.Skills = append(a.Config.Skills, Skill{ID: id, Name: id, Owner: owner, Locations: []string{location}, Snapshot: snapshot})
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
}

func requirePlan(t *testing.T, a *App) Plan {
	t.Helper()
	p, err := a.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Issues) > 0 {
		t.Fatalf("plan issues: %v", p.Issues)
	}
	return p
}

func TestApplyAndRollbackFullOriginalDirectory(t *testing.T) {
	a := applyFixture(t)
	location := ".agents/skills/example"
	path := filepath.Join(a.Home, location)
	applyPut(t, filepath.Join(path, "SKILL.md"), "local old\n")
	applyPut(t, filepath.Join(path, ".git/config"), "git history must survive\n")
	applyPut(t, filepath.Join(path, "extra.txt"), "local helper\n")
	unknown := filepath.Join(a.Home, ".agents/skills/untracked/SKILL.md")
	applyPut(t, unknown, "leave me\n")
	_, original, err := pathFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	addAccepted(t, a, "example", "manual", location, "accepted\n")
	p := requirePlan(t, a)
	if len(p.Actions) != 1 || p.Actions[0].Kind != "replace" {
		t.Fatalf("plan: %+v", p)
	}
	id, err := a.Apply(context.Background(), p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("missing transaction ID")
	}
	data, err := os.ReadFile(filepath.Join(path, "SKILL.md"))
	if err != nil || string(data) != "accepted\n" {
		t.Fatalf("apply: %q %v", data, err)
	}
	if data, _ := os.ReadFile(unknown); string(data) != "leave me\n" {
		t.Fatal("unknown skill changed")
	}
	if p := requirePlan(t, a); len(p.Actions) != 0 {
		t.Fatal("apply not idempotent")
	}
	if err := a.Rollback(id); err != nil {
		t.Fatal(err)
	}
	_, restored, err := pathFingerprint(path)
	if err != nil || restored != original {
		t.Fatalf("original not fully restored: %s %s %v", original, restored, err)
	}
	if err := a.Rollback(id); err != nil {
		t.Fatalf("repeat rollback: %v", err)
	}
}

func TestApplyMissingAndRollbackRemovesOnlyInstalledTarget(t *testing.T) {
	a := applyFixture(t)
	location := ".claude/skills/example"
	addAccepted(t, a, "example", "manual", location, "new\n")
	p := requirePlan(t, a)
	if len(p.Actions) != 1 || p.Actions[0].Kind != "install" {
		t.Fatalf("plan: %+v", p)
	}
	id, err := a.Apply(context.Background(), p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Rollback(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(a.Home, location)); !os.IsNotExist(err) {
		t.Fatal("new target not removed")
	}
}

func TestApplyPreservesExternalSymlinkAndEvidence(t *testing.T) {
	a := applyFixture(t)
	source := filepath.Join(t.TempDir(), "source")
	applyPut(t, filepath.Join(source, "SKILL.md"), "old linked contents\n")
	location := ".agents/skills/example"
	target := filepath.Join(a.Home, location)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, target); err != nil {
		t.Fatal(err)
	}
	addAccepted(t, a, "example", "manual", location, "accepted\n")
	id, err := a.Apply(context.Background(), requirePlan(t, a).Revision)
	if err != nil {
		t.Fatal(err)
	}
	if contents, _ := os.ReadFile(filepath.Join(source, "SKILL.md")); string(contents) != "old linked contents\n" {
		t.Fatal("mutated original link destination")
	}
	var record transaction
	data, err := os.ReadFile(filepath.Join(a.Repo, ".dotagents/transactions", id, "transaction.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	evidence, err := skills.NewStore(filepath.Join(a.Repo, ".dotagents/transactions", id, "evidence")).Load(record.Entries[0].OriginalSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(evidence["SKILL.md"].Data) != "old linked contents\n" {
		t.Fatal("symlink destination bytes not retained")
	}
	if err := a.Rollback(id); err != nil {
		t.Fatal(err)
	}
	if link, err := os.Readlink(target); err != nil || link != source {
		t.Fatalf("link not restored: %q %v", link, err)
	}
}

func TestApplyRejectsStalePlanAndRollbackRejectsLaterEdits(t *testing.T) {
	a := applyFixture(t)
	location := ".agents/skills/example"
	target := filepath.Join(a.Home, location)
	applyPut(t, filepath.Join(target, "SKILL.md"), "old\n")
	addAccepted(t, a, "example", "manual", location, "accepted\n")
	p := requirePlan(t, a)
	applyPut(t, filepath.Join(target, "SKILL.md"), "changed after plan\n")
	if _, err := a.Apply(context.Background(), p.Revision); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale plan accepted: %v", err)
	}
	id, err := a.Apply(context.Background(), requirePlan(t, a).Revision)
	if err != nil {
		t.Fatal(err)
	}
	applyPut(t, filepath.Join(target, "SKILL.md"), "edit after apply\n")
	if err := a.Rollback(id); err == nil || !strings.Contains(err.Error(), "changed since apply") {
		t.Fatalf("rollback overwrote later edit: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(target, "SKILL.md")); string(data) != "edit after apply\n" {
		t.Fatal("rollback destroyed later edit")
	}
}

func TestPlanRejectsEscapingAncestorsOverlapAndNonSkill(t *testing.T) {
	t.Run("escaping ancestor", func(t *testing.T) {
		a := applyFixture(t)
		if err := os.MkdirAll(filepath.Join(a.Home, ".agents"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), filepath.Join(a.Home, ".agents/skills")); err != nil {
			t.Fatal(err)
		}
		addAccepted(t, a, "example", "manual", ".agents/skills/example", "new\n")
		p, err := a.Plan()
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Issues) == 0 {
			t.Fatal("escaping ancestor accepted")
		}
	})
	t.Run("overlap", func(t *testing.T) {
		a := applyFixture(t)
		addAccepted(t, a, "one", "manual", ".agents/skills/one", "new\n")
		second := a.Config.Skills[0]
		second.ID, second.Name, second.Locations = "two", "two", []string{".agents/skills/one/skills/two"}
		a.Config.Skills = append(a.Config.Skills, second)
		p, err := a.Plan()
		if err == nil && len(p.Issues) == 0 {
			t.Fatal("overlapping destinations accepted")
		}
	})
	t.Run("non skill", func(t *testing.T) {
		a := applyFixture(t)
		applyPut(t, filepath.Join(a.Home, ".agents/skills/example/precious"), "not a skill")
		addAccepted(t, a, "example", "manual", ".agents/skills/example", "new\n")
		p, err := a.Plan()
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Issues) == 0 {
			t.Fatal("non skill replacement accepted")
		}
	})
}

func TestPlanSkipsUnselectedAndNative(t *testing.T) {
	a := applyFixture(t)
	addAccepted(t, a, "native", "native", ".codex/skills/native", "new\n")
	a.Config.Groups["never"] = Condition{Hostnames: []string{"nonexistent-test-host.invalid"}}
	addAccepted(t, a, "not-here", "manual", ".agents/skills/not-here", "new\n")
	a.Config.Skills[1].Groups = []string{"never"}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	p, err := a.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 0 || len(p.Issues) != 1 || !strings.Contains(p.Issues[0], "native") {
		t.Fatalf("native/group rules ignored: %+v", p)
	}
}

func TestPlanDelegatesSkillshareChanges(t *testing.T) {
	a := applyFixture(t)
	location := ".agents/skills/example"
	target := filepath.Join(a.Home, location)
	applyPut(t, filepath.Join(target, "SKILL.md"), "changed through native app\n")
	addAccepted(t, a, "example", "skillshare", location, "accepted\n")
	p := requirePlan(t, a)
	if len(p.Actions) != 0 || len(p.Delegated) != 1 || p.Delegated[0] != "example" {
		t.Fatalf("native-owned skill was not delegated: %+v", p)
	}
	if _, err := a.Apply(context.Background(), p.Revision); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil || string(data) != "changed through native app\n" {
		t.Fatal("dotagents overwrote native app changes")
	}
	a.Config.Skills[0].Scope = "project"
	p = requirePlan(t, a)
	if len(p.Actions) != 0 || len(p.Delegated) != 1 {
		t.Fatalf("native removals must be delegated: %+v", p)
	}
}

func TestApplyRejectsCatalogChangedAfterPreview(t *testing.T) {
	a := applyFixture(t)
	addAccepted(t, a, "example", "manual", ".agents/skills/example", "accepted\n")
	p := requirePlan(t, a)
	other, err := Open(a.Repo, a.Home)
	if err != nil {
		t.Fatal(err)
	}
	other.Config.Groups["new-group"] = Condition{OS: []string{"linux"}}
	if err = other.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.Apply(context.Background(), p.Revision); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("catalog change ignored: %v", err)
	}
	if _, err = os.Lstat(filepath.Join(a.Home, ".agents/skills/example")); !os.IsNotExist(err) {
		t.Fatal("stale apply changed live home")
	}
}

func TestRollbackRejectsDamagedBackupBeforeAnyChange(t *testing.T) {
	a := applyFixture(t)
	path := filepath.Join(a.Home, ".agents/skills/example")
	applyPut(t, filepath.Join(path, "SKILL.md"), "original\n")
	addAccepted(t, a, "example", "manual", ".agents/skills/example", "accepted\n")
	id, err := a.Apply(context.Background(), requirePlan(t, a).Revision)
	if err != nil {
		t.Fatal(err)
	}
	applyPut(t, filepath.Join(a.Repo, ".dotagents/transactions", id, "backups/0000/SKILL.md"), "damaged backup\n")
	if err = a.Rollback(id); err == nil || !strings.Contains(err.Error(), "backup damaged") {
		t.Fatalf("damaged backup accepted: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(path, "SKILL.md")); string(data) != "accepted\n" {
		t.Fatal("damaged-backup rollback changed live state")
	}
}

func installAcceptedFixture(t *testing.T, a *App, index int) {
	t.Helper()
	entry := a.Config.Skills[index]
	tree, err := a.Store().Load(entry.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeTree(filepath.Join(a.Home, entry.Locations[0]), tree); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveOutOfScopeAliasesAndRollback(t *testing.T) {
	a := applyFixture(t)
	addAccepted(t, a, "example", "manual", ".agents/skills/example", "accepted\n")
	installAcceptedFixture(t, a, 0)
	alias := filepath.Join(a.Home, ".codex/skills/example")
	if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.agents/skills/example", alias); err != nil {
		t.Fatal(err)
	}
	a.Config.Skills[0].Locations = append(a.Config.Skills[0].Locations, ".codex/skills/example")
	a.Config.Skills[0].Scope = "project"
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(a.Home, ".agents/skills/unknown/SKILL.md")
	applyPut(t, unknown, "untracked\n")
	p := requirePlan(t, a)
	if len(p.Actions) != 2 {
		t.Fatalf("expected both removals: %+v", p)
	}
	for _, action := range p.Actions {
		if action.Kind != "remove" {
			t.Fatalf("wrong action: %+v", action)
		}
	}
	t.Setenv("PATH", t.TempDir()) // Removal does not need to execute Skillshare.
	id, err := a.Apply(context.Background(), p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range a.Config.Skills[0].Locations {
		if _, err := os.Lstat(filepath.Join(a.Home, loc)); !os.IsNotExist(err) {
			t.Fatalf("out-of-scope location remains: %s", loc)
		}
	}
	if data, _ := os.ReadFile(unknown); string(data) != "untracked\n" {
		t.Fatal("unknown was changed")
	}
	if p := requirePlan(t, a); len(p.Actions) != 0 {
		t.Fatal("absent out-of-scope locations should be a no-op")
	}
	if err := a.Rollback(id); err != nil {
		t.Fatal(err)
	}
	if link, err := os.Readlink(alias); err != nil || link != "../../.agents/skills/example" {
		t.Fatalf("alias not restored: %q %v", link, err)
	}
	if data, err := os.ReadFile(filepath.Join(alias, "SKILL.md")); err != nil || string(data) != "accepted\n" {
		t.Fatalf("alias contents not restored: %q %v", data, err)
	}
}

func TestRemoveRefusesEditedContents(t *testing.T) {
	a := applyFixture(t)
	addAccepted(t, a, "example", "manual", ".agents/skills/example", "accepted\n")
	installAcceptedFixture(t, a, 0)
	a.Config.Skills[0].Scope = "project"
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.Home, ".agents/skills/example/references/guide.md")
	applyPut(t, path, "local reference edit\n")
	p, err := a.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Issues) == 0 || len(p.Actions) != 0 {
		t.Fatalf("edited removal wasn't blocked: %+v", p)
	}
	if _, err := a.Apply(context.Background(), p.Revision); err == nil {
		t.Fatal("edited out-of-scope skill was removed")
	}
	if data, _ := os.ReadFile(path); string(data) != "local reference edit\n" {
		t.Fatal("edited contents were lost")
	}
}

func TestRemoveDoesNotBreakUntrackedAliases(t *testing.T) {
	a := applyFixture(t)
	addAccepted(t, a, "example", "manual", ".agents/skills/example", "accepted\n")
	installAcceptedFixture(t, a, 0)
	alias := filepath.Join(a.Home, ".claude/skills/unknown-alias")
	if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.agents/skills/example", alias); err != nil {
		t.Fatal(err)
	}
	a.Config.Skills[0].Scope = "project"
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	p, err := a.Plan()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Issues) == 0 || !strings.Contains(strings.Join(p.Issues, " "), "would break") {
		t.Fatalf("untracked alias would be stranded: %+v", p)
	}
	if _, err := a.Apply(context.Background(), p.Revision); err == nil {
		t.Fatal("removed source of untracked alias")
	}
	if _, err := os.Stat(filepath.Join(alias, "SKILL.md")); err != nil {
		t.Fatal("unknown alias stopped working")
	}
}

func TestRemoveIndependentAliasLeavesSourceAndOtherAlias(t *testing.T) {
	a := applyFixture(t)
	addAccepted(t, a, "example", "manual", ".agents/skills/example", "accepted\n")
	source := filepath.Join(t.TempDir(), "source")
	tree, err := a.Store().Load(a.Config.Skills[0].Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeTree(source, tree); err != nil {
		t.Fatal(err)
	}
	for _, loc := range []string{".agents/skills/example", ".claude/skills/unknown-alias"} {
		path := filepath.Join(a.Home, loc)
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.Symlink(source, path); err != nil {
			t.Fatal(err)
		}
	}
	a.Config.Skills[0].Scope = "project"
	if err = a.Save(); err != nil {
		t.Fatal(err)
	}
	id, err := a.Apply(context.Background(), requirePlan(t, a).Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(a.Home, ".claude/skills/unknown-alias/SKILL.md")); err != nil {
		t.Fatal("independent unknown alias broke")
	}
	if err = a.Rollback(id); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveRollbackDoesNotOverwriteReplacement(t *testing.T) {
	a := applyFixture(t)
	addAccepted(t, a, "example", "manual", ".agents/skills/example", "accepted\n")
	installAcceptedFixture(t, a, 0)
	a.Config.Skills[0].Scope = "project"
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	id, err := a.Apply(context.Background(), requirePlan(t, a).Revision)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.Home, ".agents/skills/example/SKILL.md")
	applyPut(t, path, "new unrelated installation\n")
	if err = a.Rollback(id); err == nil {
		t.Fatal("rollback replaced a new installation at a removed path")
	}
	if data, _ := os.ReadFile(path); string(data) != "new unrelated installation\n" {
		t.Fatal("new installation overwritten")
	}
}

func TestRollbackAfterPartialPublication(t *testing.T) {
	a := applyFixture(t)
	for _, name := range []string{"one", "two"} {
		location := ".agents/skills/" + name
		applyPut(t, filepath.Join(a.Home, location, "SKILL.md"), "original "+name+"\n")
		addAccepted(t, a, name, "manual", location, "accepted\n")
	}
	p := requirePlan(t, a)
	id := "partial-test"
	dir := filepath.Join(a.Repo, ".dotagents/transactions", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	record := transaction{Version: 1, ID: id, Home: a.Home, Repo: a.Repo, Revision: p.Revision}
	if err := a.prepareTransaction(context.Background(), dir, p, &record); err != nil {
		t.Fatal(err)
	}
	record.Status = "applying"
	if err := writeTransaction(dir, &record); err != nil {
		t.Fatal(err)
	}
	if err := publishEntry(a.Home, dir, record.Entries[0]); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after one publication, before the next target.
	if err := a.Rollback(id); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		data, err := os.ReadFile(filepath.Join(a.Home, ".agents/skills", name, "SKILL.md"))
		if err != nil || string(data) != "original "+name+"\n" {
			t.Fatalf("partial rollback lost %s: %s %v", name, data, err)
		}
	}
}

func TestApplySharedAliasesAndRestoreOriginalLink(t *testing.T) {
	a := applyFixture(t)
	primary := ".agents/skills/example"
	alias := ".claude/skills/example"
	applyPut(t, filepath.Join(a.Home, primary, "SKILL.md"), "old shared version\n")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(a.Home, alias)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.agents/skills/example", filepath.Join(a.Home, alias)); err != nil {
		t.Fatal(err)
	}
	addAccepted(t, a, "example", "manual", primary, "new accepted version\n")
	a.Config.Skills[0].Locations = append(a.Config.Skills[0].Locations, alias)
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	p := requirePlan(t, a)
	if len(p.Actions) != 2 {
		t.Fatalf("want both locations: %+v", p)
	}
	id, err := a.Apply(context.Background(), p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{primary, alias} {
		data, err := os.ReadFile(filepath.Join(a.Home, location, "SKILL.md"))
		if err != nil || string(data) != "new accepted version\n" {
			t.Fatalf("alias publication failed: %s %v", data, err)
		}
	}
	if err := a.Rollback(id); err != nil {
		t.Fatal(err)
	}
	if link, err := os.Readlink(filepath.Join(a.Home, alias)); err != nil || link != "../../.agents/skills/example" {
		t.Fatalf("original alias lost: %s %v", link, err)
	}
	if data, _ := os.ReadFile(filepath.Join(a.Home, alias, "SKILL.md")); string(data) != "old shared version\n" {
		t.Fatal("alias source not restored")
	}
}

type cancelDuringApply struct{ checks int }

func (c *cancelDuringApply) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cancelDuringApply) Done() <-chan struct{}       { return nil }
func (c *cancelDuringApply) Value(any) any               { return nil }
func (c *cancelDuringApply) Err() error {
	c.checks++
	if c.checks >= 4 {
		return context.Canceled
	}
	return nil
}

func TestApplyCancellationRestoresEarlierWrites(t *testing.T) {
	a := applyFixture(t)
	for _, name := range []string{"one", "two"} {
		location := ".agents/skills/" + name
		applyPut(t, filepath.Join(a.Home, location, "SKILL.md"), "original "+name+"\n")
		addAccepted(t, a, name, "manual", location, "accepted\n")
	}
	id, err := a.Apply(&cancelDuringApply{}, requirePlan(t, a).Revision)
	if err == nil || !strings.Contains(err.Error(), "original targets were restored") {
		t.Fatalf("cancellation not rolled back: %s %v", id, err)
	}
	for _, name := range []string{"one", "two"} {
		data, err := os.ReadFile(filepath.Join(a.Home, ".agents/skills", name, "SKILL.md"))
		if err != nil || string(data) != "original "+name+"\n" {
			t.Fatalf("lost original %s: %s %v", name, data, err)
		}
	}
}

func TestBackupPreservesReadOnlyDirectoryModes(t *testing.T) {
	a := applyFixture(t)
	path := filepath.Join(a.Home, ".agents/skills/example")
	applyPut(t, filepath.Join(path, "SKILL.md"), "original\n")
	if err := os.Chmod(path, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(filepath.Dir(a.Repo), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0755)
			}
			return nil
		})
	})
	addAccepted(t, a, "example", "manual", ".agents/skills/example", "accepted\n")
	id, err := a.Apply(context.Background(), requirePlan(t, a).Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Rollback(id); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatalf("lost directory mode: %v %v", info, err)
	}
}

func TestRenameRestoresReadOnlyModeOnSuccessAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			destination := filepath.Join(root, "destination")
			applyPut(t, filepath.Join(source, "SKILL.md"), "unchanged\n")
			if err := os.Chmod(source, 0555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.Chmod(source, 0755)
				_ = os.Chmod(destination, 0755)
			})
			calls := 0
			err := renameWithDirectoryPermissions(source, destination, func(from, to string) error {
				calls++
				info, err := os.Stat(from)
				if err != nil {
					return err
				}
				if info.Mode().Perm()&0200 == 0 {
					return &os.LinkError{Op: "rename", Old: from, New: to, Err: fs.ErrPermission}
				}
				if fail {
					return fs.ErrExist
				}
				return os.Rename(from, to)
			})
			if calls != 2 || (fail && !errors.Is(err, fs.ErrExist)) || (!fail && err != nil) {
				t.Fatalf("rename result: calls=%d error=%v", calls, err)
			}
			retained := destination
			if fail {
				retained = source
			}
			info, err := os.Stat(retained)
			if err != nil || info.Mode().Perm() != 0555 {
				t.Fatalf("original permissions not restored: %v %v", info, err)
			}
			data, err := os.ReadFile(filepath.Join(retained, "SKILL.md"))
			if err != nil || string(data) != "unchanged\n" {
				t.Fatalf("original bytes not retained: %s %v", data, err)
			}
		})
	}
}

func TestRenamePermissionRecoveryNeverChmodsLinkTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(target, 0755) })
	source := filepath.Join(root, "source")
	if err := os.Symlink(target, source); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := renameWithDirectoryPermissions(source, filepath.Join(root, "destination"), func(string, string) error {
		calls++
		return fs.ErrPermission
	})
	if !errors.Is(err, fs.ErrPermission) || calls != 1 {
		t.Fatalf("symlink retried as directory: calls=%d error=%v", calls, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatalf("link target permissions changed: %v %v", info, err)
	}
}
