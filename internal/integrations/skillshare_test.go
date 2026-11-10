package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func fakeSkillshare(t *testing.T, mode string) string {
	t.Helper()
	directory := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program := "#!/bin/sh\nexec \"${DOTAGENTS_HELPER_BINARY}\" -test.run=TestSkillshareHelper -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(directory, "skillshare"), []byte(program), 0755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(directory, "staging-path")
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOTAGENTS_HELPER_BINARY", binary)
	t.Setenv("DOTAGENTS_HELPER_MODE", mode)
	t.Setenv("DOTAGENTS_HELPER_HOME", os.Getenv("HOME"))
	t.Setenv("DOTAGENTS_HELPER_RECORD", record)
	t.Setenv("SKILLSHARE_SOURCE", "/must-not-use-this-live-source")
	return record
}

func TestFetchUsesIsolatedSkillshareAndVerifiesContents(t *testing.T) {
	record := fakeSkillshare(t, "success")
	result, err := Fetch(context.Background(), "owner/repository", "example", testCommit)
	if err != nil {
		t.Fatal(err)
	}
	if result.Commit != testCommit || string(result.Files["SKILL.md"].Data) != "# Example\n" || result.Source != "owner/repository" {
		t.Fatalf("incorrect fetch: %+v", result)
	}
	if _, hasTimestamp := result.Metadata["installed_at"]; hasTimestamp {
		t.Fatal("volatile installation timestamp leaked into provenance")
	}
	stage, err := os.ReadFile(record)
	if err != nil {
		t.Fatal("helper did not record staging directory", err)
	}
	if _, err := os.Stat(string(stage)); !os.IsNotExist(err) {
		t.Fatal("fetch left its staging directory behind")
	}
}

func TestFetchRejectsUnpinnedAndUnsafeInputsBeforeExecution(t *testing.T) {
	record := fakeSkillshare(t, "success")
	for _, input := range [][3]string{
		{"owner/repo", "example", "main"},
		{"owner/repo", "../escape", testCommit},
		{"--force", "example", testCommit},
		{"https://token@github.com/owner/repo", "example", testCommit},
		{"owner/repo/../other", "example", testCommit},
	} {
		if _, err := Fetch(context.Background(), input[0], input[1], input[2]); err == nil {
			t.Fatalf("accepted invalid input: %v", input)
		}
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatal("invalid input invoked installer")
	}
}

func TestFetchFailsClosedOnInstallerAndProvenanceFailures(t *testing.T) {
	for _, mode := range []string{"audit-block", "wrong-commit", "wrong-hash", "extra-skill", "escaped-skill"} {
		t.Run(mode, func(t *testing.T) {
			fakeSkillshare(t, mode)
			if _, err := Fetch(context.Background(), "owner/repository", "example", testCommit); err == nil {
				t.Fatal("accepted rejected or unverifiable install")
			}
		})
	}
}

func TestFetchHonorsCancelledContext(t *testing.T) {
	record := fakeSkillshare(t, "success")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Fetch(ctx, "owner/repository", "example", testCommit); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatal("cancelled fetch invoked installer")
	}
}

func TestBoundedOutput(t *testing.T) {
	var output boundedOutput
	input := []byte(strings.Repeat("x", 128<<10))
	if n, err := output.Write(input); err != nil || n != len(input) {
		t.Fatal("bounded writer did not consume output")
	}
	if len(output.String()) > (65<<10) || !strings.Contains(output.String(), "truncated") {
		t.Fatal("installer output is not bounded")
	}
}

func TestFetchRealPinnedPublicSkill(t *testing.T) {
	if os.Getenv("DOTAGENTS_TEST_FETCH") != "1" {
		t.Skip("set DOTAGENTS_TEST_FETCH=1 to fetch the pinned public Cloudflare fixture")
	}
	const commit = "6eb6c781aa9b6438e0c7307bd102788a75fe1a6d"
	result, err := Fetch(context.Background(), "cloudflare/skills", "cf-cli", commit)
	if err != nil {
		t.Fatal(err)
	}
	if result.Commit != commit || len(result.Files) != 2 || !strings.Contains(string(result.Files["SKILL.md"].Data), "name: cf-cli") {
		t.Fatalf("unexpected real fetch: commit=%s files=%d", result.Commit, len(result.Files))
	}
}

// TestSkillshareHelper runs only in a child test process launched by the fake
// executable. It verifies staging settings before writing fake installer output.
func TestSkillshareHelper(t *testing.T) {
	mode := os.Getenv("DOTAGENTS_HELPER_MODE")
	if mode == "" || !slices.Contains(os.Args, "--") {
		return
	}
	fail := func(message string) {
		fmt.Fprintln(os.Stderr, message)
		os.Exit(3)
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if len(args) < 2 || args[0] != "install" || args[1] != "owner/repository" || slices.Contains(args, "--force") || slices.Contains(args, "--skip-audit") || !slices.Contains(args, "-g") || !slices.Contains(args, "--skill") {
		fail("unexpected or unsafe installer invocation")
	}
	config := os.Getenv("SKILLSHARE_CONFIG")
	stage := filepath.Dir(filepath.Dir(config))
	cwd, _ := os.Getwd()
	stageReal, _ := filepath.EvalSymlinks(stage)
	if cwd != stageReal && cwd != stage {
		fail("installer did not run inside staging directory")
	}
	if os.Getenv("HOME") != os.Getenv("DOTAGENTS_HELPER_HOME") || os.Getenv("SKILLSHARE_SOURCE") != "" {
		fail("HOME changed or inherited source override leaked")
	}
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if !strings.HasPrefix(os.Getenv(key), stage+string(filepath.Separator)) {
			fail("XDG paths did not remain isolated")
		}
	}
	data, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(data), "targets: {}") {
		fail("staging config has targets or is unreadable")
	}
	if err := os.WriteFile(os.Getenv("DOTAGENTS_HELPER_RECORD"), []byte(stage), 0600); err != nil {
		fail("could not record staging path")
	}
	if mode == "audit-block" {
		fail("security audit blocked the install")
	}
	library := filepath.Join(stage, "library")
	path := filepath.Join(library, "example")
	if mode == "escaped-skill" {
		outside := filepath.Join(stage, "outside")
		_ = os.MkdirAll(outside, 0700)
		_ = os.Symlink(outside, path)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		fail("could not create skill")
	}
	content := []byte("# Example\n")
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), content, 0644); err != nil {
		fail("could not write skill")
	}
	hash := sha256.Sum256(content)
	commit := testCommit
	if mode == "wrong-commit" {
		commit = strings.Repeat("a", 40)
	}
	digest := "sha256:" + hex.EncodeToString(hash[:])
	if mode == "wrong-hash" {
		digest = "sha256:bad"
	}
	entries := map[string]any{"example": map[string]any{"commit": commit, "installed_at": "today", "file_hashes": map[string]string{"SKILL.md": digest}}}
	if mode == "extra-skill" {
		entries["unexpected"] = entries["example"]
	}
	metadata, _ := json.Marshal(map[string]any{"version": 1, "entries": entries})
	if err := os.WriteFile(filepath.Join(library, ".metadata.json"), metadata, 0600); err != nil {
		fail("could not write metadata")
	}
	os.Exit(0)
}
