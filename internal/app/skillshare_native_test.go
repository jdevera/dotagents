package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdevera/dotagents/internal/skills"
)

func nativeFixtureSkill(t *testing.T, a *App, id, location string) {
	t.Helper()
	addAccepted(t, a, id, "skillshare", location, "---\nname: "+id+"\ndescription: Isolated native integration fixture.\n---\nAccepted contents.\n")
	a.Config.Skills[len(a.Config.Skills)-1].Source = "example/skills"
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
}
func nativeSeed(t *testing.T, a *App, id string) {
	t.Helper()
	for _, entry := range a.Config.Skills {
		if entry.ID == id {
			tree, err := a.Store().Load(entry.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err = writeTree(filepath.Join(a.Repo, "skills", entry.Name), tree); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("missing fixture skill")
}
func requireNativeCLI(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("skillshare"); err != nil {
		t.Skip("Skillshare integration requires installed CLI")
	}
}
func nativePlan(t *testing.T, a *App) NativePlan {
	t.Helper()
	plan, err := a.NativeSkillsharePlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func nativeConnect(t *testing.T, a *App, phase string) NativeSkillshareResult {
	t.Helper()
	plan := nativePlan(t, a)
	result, err := a.ConnectSkillshare(context.Background(), phase, plan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNativePlanShowsMigrationAndDoesNotMutate(t *testing.T) {
	a := applyFixture(t)
	nativeFixtureSkill(t, a, "example", ".agents/skills/example")
	tree, err := a.Store().Load(a.Config.Skills[0].Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.Home, ".agents/skills/example")
	if err = writeTree(path, tree); err != nil {
		t.Fatal(err)
	}
	plan := nativePlan(t, a)
	if plan.Ready || plan.Initialized || len(plan.Imports) != 1 || len(plan.Collisions) != 1 || !plan.Collisions[0].MatchesAccepted {
		t.Fatalf("incorrect migration plan: %+v", plan)
	}
	if _, err = os.Stat(a.SkillshareConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("plan wrote native config: %v", err)
	}
	applyPut(t, filepath.Join(path, "SKILL.md"), "changed after preview")
	_, err = a.ConnectSkillshare(context.Background(), "prepare", plan.Revision)
	if err == nil || !strings.Contains(err.Error(), "plan changed") {
		t.Fatalf("stale plan accepted: %v", err)
	}
}

func TestNativeSkillshareOwnsPublicationAndPreservesAuditBaseline(t *testing.T) {
	requireNativeCLI(t)
	ctx := context.Background()
	a := applyFixture(t)
	nativeFixtureSkill(t, a, "alpha", ".claude/skills/alpha")
	nativeFixtureSkill(t, a, "work-only", ".claude/skills/work-only")
	a.Config.Groups["work"] = Condition{Hostnames: []string{"not-this-machine.invalid"}}
	a.Config.Skills[1].Groups = []string{"work"}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	nativeSeed(t, a, "alpha")
	nativeSeed(t, a, "work-only")
	before := a.Config.Skills[0].Snapshot
	source := filepath.Join(a.Repo, "skills/alpha/SKILL.md")
	applyPut(t, source, "---\nname: alpha\ndescription: Native source changes.\n---\nUpdated in Skillshare.\n")
	prepared := nativeConnect(t, a, "prepare")
	if !prepared.Plan.Initialized || prepared.Plan.Ready {
		t.Fatalf("prepare reported deployment: %+v", prepared.Plan)
	}
	if len(prepared.Plan.Targets) != 1 || prepared.Plan.Targets[0].Existing {
		t.Fatal("prepare must not add targets")
	}
	if _, err := os.Stat(filepath.Join(a.Home, ".claude/skills/alpha")); !os.IsNotExist(err) {
		t.Fatalf("prepare changed agent directory: %v", err)
	}
	if len(prepared.Commands) != 1 || !strings.Contains(prepared.Commands[0], "init") {
		t.Fatalf("existing source was reinstalled: %v", prepared.Commands)
	}
	policy := nativeConnect(t, a, "policy")
	if policy.Plan.Ready || len(policy.Plan.Pending) != 1 {
		t.Fatalf("policy claimed native publication: %+v", policy.Plan)
	}
	if _, err := os.Stat(filepath.Join(a.Home, ".claude/skills/alpha")); !os.IsNotExist(err) {
		t.Fatalf("target add implicitly synced: %v", err)
	}
	if _, err := a.SkillshareCommand(ctx, "target", "claude", "--add-exclude", "manual-*"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SkillshareCommand(ctx, "sync"); err != nil {
		t.Fatal(err)
	}
	connected := nativePlan(t, a)
	if !connected.Ready || len(connected.Collisions) != 0 {
		t.Fatalf("native links not recognized as connected: %+v", connected)
	}
	if _, err := os.Stat(filepath.Join(a.Home, ".claude/skills/work-only")); !os.IsNotExist(err) {
		t.Fatalf("excluded skill installed: %v", err)
	}
	report := a.Report(ctx)
	changed := false
	for _, finding := range report.Findings {
		if finding.ID == "alpha" && finding.Status == "changed" {
			changed = true
		}
	}
	if !changed || a.Config.Skills[0].Snapshot != before {
		t.Fatal("native updates were not audited against unchanged accepted baseline")
	}
	a.Config.Groups["work"] = Condition{}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	a, err := a.Reload()
	if err != nil {
		t.Fatal(err)
	}
	next := nativeConnect(t, a, "policy")
	if next.Plan.Ready || len(next.Plan.Pending) != 1 {
		t.Fatalf("new machine selection not pending: %+v", next.Plan)
	}
	target := next.Plan.Targets[0]
	if !contains(target.Exclude, "manual-*") || contains(target.Exclude, "work-only") {
		t.Fatalf("owned filter reconciliation damaged manual choices: %+v", target)
	}
	if _, err := a.SkillshareCommand(ctx, "sync"); err != nil {
		t.Fatal(err)
	}
	if plan := nativePlan(t, a); !plan.Ready {
		t.Fatalf("native sync did not connect: %+v", plan)
	}
	data, err := os.ReadFile(source)
	if err != nil || !strings.Contains(string(data), "Updated in Skillshare") {
		t.Fatal("connect overwrote native source changes")
	}
}

func TestNativePolicyRefusesEditedCollision(t *testing.T) {
	requireNativeCLI(t)
	a := applyFixture(t)
	nativeFixtureSkill(t, a, "example", ".agents/skills/example")
	nativeSeed(t, a, "example")
	nativeConnect(t, a, "prepare")
	applyPut(t, filepath.Join(a.Home, ".agents/skills/example/SKILL.md"), "unreviewed local edit")
	plan := nativePlan(t, a)
	_, err := a.ConnectSkillshare(context.Background(), "policy", plan.Revision)
	if err == nil || !strings.Contains(err.Error(), "edited or unreadable") {
		t.Fatalf("edited collision not refused: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(a.Home, ".agents/skills/example/SKILL.md"))
	if string(data) != "unreviewed local edit" {
		t.Fatal("collision overwritten")
	}
}

func TestNativeBackupPreservesBytesPrivately(t *testing.T) {
	a := applyFixture(t)
	path := filepath.Join(a.Home, ".config/skillshare/config.yaml")
	applyPut(t, path, "private-config: keep-exactly\n")
	backup, err := a.BackupNativeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(backup)
	if err != nil || string(data) != "private-config: keep-exactly\n" {
		t.Fatalf("wrong backup: %s %v", data, err)
	}
	info, err := os.Stat(backup)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("insecure backup: %v %v", info, err)
	}
	var receipt struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	data, err = os.ReadFile(filepath.Join(filepath.Dir(backup), "receipt.json"))
	if err != nil || json.Unmarshal(data, &receipt) != nil || receipt.Path != path || len(receipt.SHA256) != 64 {
		t.Fatal("invalid receipt")
	}
}

func TestNativeSourceValidation(t *testing.T) {
	for _, source := range []string{"owner/repo", "https://github.com/owner/repo.git", "git@github.com:owner/repo/skills/example"} {
		if !nativeSource(source) {
			t.Fatalf("rejected %q", source)
		}
	}
	for _, source := range []string{"file:///tmp/source", "https://evil.example/repo", "owner/../repo", "owner/repo?token=secret", "owner/repo\n--force", "--bad/repo"} {
		if nativeSource(source) {
			t.Fatalf("accepted %q", source)
		}
	}
}

func TestNativeSkillTreeComparisonIncludesHelperFiles(t *testing.T) {
	a := applyFixture(t)
	nativeFixtureSkill(t, a, "example", ".claude/skills/example")
	nativeSeed(t, a, "example")
	applyPut(t, filepath.Join(a.Repo, "skills/example/references/guide.md"), "native helper changed\n")
	plan := nativePlan(t, a)
	if len(plan.Imports) != 1 || plan.Imports[0].Changes != 1 {
		t.Fatalf("helper drift not reported: %+v", plan.Imports)
	}
	original, err := a.Store().Load(a.Config.Skills[0].Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	current, err := skills.ReadTree(filepath.Join(a.Repo, "skills/example"))
	if err != nil {
		t.Fatal(err)
	}
	if len(skills.Diff(original, current)) != 1 {
		t.Fatal("source mismatch")
	}
}

func TestNativePlanRejectsDiscoverySourceAndManualPolicyConflicts(t *testing.T) {
	requireNativeCLI(t)
	t.Run("canonical source", func(t *testing.T) {
		a := applyFixture(t)
		root := filepath.Join(a.Home, ".agents/skills")
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(root, filepath.Join(a.Repo, "skills")); err != nil {
			t.Fatal(err)
		}
		plan := nativePlan(t, a)
		if len(plan.Issues) == 0 || !strings.Contains(plan.Issues[0], "agent discovery root") {
			t.Fatalf("discovery-root source accepted: %+v", plan)
		}
	})
	for _, kind := range []string{"include", "exclude", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			a := applyFixture(t)
			nativeFixtureSkill(t, a, "example", ".claude/skills/example")
			nativeSeed(t, a, "example")
			nativeConnect(t, a, "prepare")
			nativeConnect(t, a, "policy")
			var args []string
			switch kind {
			case "include":
				args = []string{"target", "claude", "--add-include", "unrelated-*"}
			case "exclude":
				args = []string{"target", "claude", "--add-exclude", "example"}
			case "disabled":
				args = []string{"target", "claude", "--skills=false"}
			}
			if _, err := a.SkillshareCommand(context.Background(), args...); err != nil {
				t.Fatal(err)
			}
			plan := nativePlan(t, a)
			if len(plan.Issues) == 0 || plan.Ready {
				t.Fatalf("manual native policy conflict ignored: %+v", plan)
			}
			if _, err := a.ConnectSkillshare(context.Background(), "policy", plan.Revision); err == nil {
				t.Fatal("native choice overwritten")
			}
		})
	}
}
