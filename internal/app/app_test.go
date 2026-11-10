package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdevera/dotagents/internal/skills"
)

func testApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	a := &App{Repo: repo, Home: home, Config: Config{Version: 1, Groups: map[string]Condition{"work": {Hostnames: []string{"work-*"}}, "mac": {OS: []string{"darwin"}}}, Skills: []Skill{}, Ignore: []Ignore{}, MCP: []MCPRule{}}, Machine: Machine{Hostname: "work-example", OS: "darwin", Arch: "arm64", Home: home, Groups: []string{"mac", "work"}}}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	return a
}

func put(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func finding(t *testing.T, r Report, id string) Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("missing finding %s: %#v", id, r.Findings)
	return Finding{}
}

func TestConditionCombinations(t *testing.T) {
	m := Machine{Hostname: "work-laptop", OS: "darwin", Arch: "arm64", Home: t.TempDir()}
	t.Setenv("DOTAGENTS_TEST_ROLE", "developer")
	put(t, filepath.Join(m.Home, "marker"), "")
	c := Condition{OS: []string{"darwin", "linux"}, Hostnames: []string{"work-*"}, Env: map[string]string{"DOTAGENTS_TEST_ROLE": "developer"}, Exists: []string{"~/marker"}, Any: []Condition{{Arch: []string{"arm64"}}, {Arch: []string{"amd64"}}}, Not: &Condition{Hostnames: []string{"*-retired"}}}
	if !c.Match(m) {
		t.Fatal("matching conditions rejected")
	}
	m.Hostname = "personal-laptop"
	if c.Match(m) {
		t.Fatal("hostname condition ignored")
	}
	if err := (Condition{Hostnames: []string{"["}}).Validate(0); err == nil {
		t.Fatal("malformed pattern accepted")
	}
}

func TestUnknownAdoptEditAndAccept(t *testing.T) {
	a := testApp(t)
	path := filepath.Join(a.Home, ".agents/skills/example")
	put(t, filepath.Join(path, "SKILL.md"), "---\nname: example\n---\nOriginal\n")
	put(t, filepath.Join(path, "references/guide.txt"), "Old guide\n")
	r := a.Report(context.Background())
	if r.Counts["unknown"] != 1 {
		t.Fatalf("expected unknown skill, got %#v", r.Counts)
	}
	if _, err := a.Adopt(path, "example", "manual", "", nil); err != nil {
		t.Fatal(err)
	}
	if f := finding(t, a.Report(context.Background()), "example"); f.Status != "ok" {
		t.Fatalf("adoption did not establish baseline: %#v", f)
	}
	put(t, filepath.Join(path, "references/guide.txt"), "Changed guide\n")
	f := finding(t, a.Report(context.Background()), "example")
	if f.Status != "changed" || len(f.InstalledChanges) != 1 || !strings.Contains(f.InstalledChanges[0].Diff, "Changed guide") {
		t.Fatalf("helper file drift lost: %#v", f)
	}
	oldSnapshot := a.Config.Skills[0].Snapshot
	if err := a.Accept(context.Background(), "example", "installed"); err != nil {
		t.Fatal(err)
	}
	if a.Config.Skills[0].Snapshot == oldSnapshot {
		t.Fatal("accept didn't change snapshot")
	}
	old, err := a.Store().Load(oldSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(old["references/guide.txt"].Data) != "Old guide\n" {
		t.Fatal("old accepted version lost")
	}
	if f := finding(t, a.Report(context.Background()), "example"); f.Status != "ok" {
		t.Fatalf("new baseline not clean: %#v", f)
	}
}

func TestMissingUnexpectedAndAliasDiscovery(t *testing.T) {
	a := testApp(t)
	p := filepath.Join(a.Home, ".agents/skills/test")
	put(t, filepath.Join(p, "SKILL.md"), "test\n")
	if _, err := a.Adopt(p, "test", "manual", "", []string{"work"}); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(a.Home, ".claude/skills/test")
	if err := os.MkdirAll(filepath.Dir(alias), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(p, alias); err != nil {
		t.Fatal(err)
	}
	r := a.Report(context.Background())
	if r.Counts["unknown"] != 1 {
		t.Fatalf("untracked alias should be visible: %#v", r.Counts)
	}
	a.Machine.Groups = []string{"mac"}
	if f := finding(t, a.Report(context.Background()), "test"); f.Status != "unexpected" {
		t.Fatalf("wanted filter ignored: %#v", f)
	}
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
	a.Machine.Groups = []string{"work"}
	if f := finding(t, a.Report(context.Background()), "test"); f.Status != "missing" {
		t.Fatalf("missing installation not detected: %#v", f)
	}
}

func TestAcceptRejectsDivergentCopies(t *testing.T) {
	a := testApp(t)
	p := filepath.Join(a.Home, ".agents/skills/test")
	put(t, filepath.Join(p, "SKILL.md"), "accepted\n")
	if _, err := a.Adopt(p, "test", "manual", "", nil); err != nil {
		t.Fatal(err)
	}
	other := ".claude/skills/test"
	put(t, filepath.Join(a.Home, other, "SKILL.md"), "different\n")
	a.Config.Skills[0].Locations = append(a.Config.Skills[0].Locations, other)
	if err := a.Accept(context.Background(), "test", "installed"); err == nil {
		t.Fatal("accepted disagreeing installations")
	}
}

func TestReadOnlyCheckDoesNotCreateState(t *testing.T) {
	a := testApp(t)
	repoBefore, err := skills.ReadTree(a.Repo)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(a.Home, ".agents/skills/test")
	put(t, filepath.Join(p, "SKILL.md"), "untouched\n")
	before, err := skills.ReadTree(a.Home)
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := Run(context.Background(), []string{"check", "--repo", a.Repo, "--home", a.Home, "--json"}, &out, &stderr)
	if code != 1 {
		t.Fatalf("want findings exit1, got %d: %s", code, stderr.String())
	}
	var report Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	after, err := skills.ReadTree(a.Home)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills.Diff(before, after)) != 0 {
		t.Fatal("check changed inspected home")
	}
	repoAfter, err := skills.ReadTree(a.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills.Diff(repoBefore, repoAfter)) != 0 {
		t.Fatal("check changed repository/runtime state")
	}
}

func TestMCPTrackAndSecretOnlyDiff(t *testing.T) {
	a := testApp(t)
	path := filepath.Join(a.Home, ".claude.json")
	put(t, path, `{"mcpServers":{"example":{"command":"server","env":{"TOKEN":"secret-one"}}}}`)
	if err := a.TrackMCP("claude", "example", nil); err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile(filepath.Join(a.Repo, "dotagents.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(catalog, []byte("secret-one")) {
		t.Fatal("credential leaked to public catalog")
	}
	put(t, path, `{"mcpServers":{"example":{"command":"server","env":{"TOKEN":"secret-two"}}}}`)
	r := a.Report(context.Background())
	f := finding(t, r, "mcp:claude:example")
	if f.Status != "changed" || len(f.InstalledChanges) == 0 {
		t.Fatalf("secret-only drift invisible: %#v", f)
	}
	encoded, _ := json.Marshal(r)
	if bytes.Contains(encoded, []byte("secret-one")) || bytes.Contains(encoded, []byte("secret-two")) {
		t.Fatal("secret leaked to report")
	}
}

func TestBackupContentsAndDigest(t *testing.T) {
	a := testApp(t)
	put(t, filepath.Join(a.Home, ".agents/skills/test/SKILL.md"), "original\n")
	put(t, filepath.Join(a.Home, ".claude.json"), "private\n")
	if err := os.MkdirAll(filepath.Join(a.Home, ".claude/skills"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../.agents/skills/test", filepath.Join(a.Home, ".claude/skills/test")); err != nil {
		t.Fatal(err)
	}
	receipt, err := a.Backup()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(receipt.Archive)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if receipt.SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatal("archive digest wrong")
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	files := map[string]string{}
	links := map[string]string{}
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			b, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			files[h.Name] = string(b)
		}
		if h.Typeflag == tar.TypeSymlink {
			links[h.Name] = h.Linkname
		}
	}
	if files["home/.agents/skills/test/SKILL.md"] != "original\n" || files["home/.claude.json"] != "private\n" {
		t.Fatal("backup omitted affected files")
	}
	if links["home/.claude/skills/test"] != "../../.agents/skills/test" {
		t.Fatal("backup lost symlink")
	}
	info, _ := os.Stat(receipt.Archive)
	if info.Mode().Perm() != 0600 {
		t.Fatal("private backup is not private")
	}
}

func TestCatalogRejectsTypoAndTraversal(t *testing.T) {
	a := testApp(t)
	put(t, filepath.Join(a.Repo, "dotagents.json"), `{"version":1,"groupz":{}}`)
	if _, err := Open(a.Repo, a.Home); err == nil {
		t.Fatal("unknown catalog field silently accepted")
	}
	if _, err := a.Adopt(filepath.Join(a.Home, "../outside/skills/foo"), "foo", "manual", "", nil); err == nil {
		t.Fatal("adopt accepted path outside home")
	}
}
