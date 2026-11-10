package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestScanDeduplicatesLinksAndPreservesDistinctNames(t *testing.T) {
	home := t.TempDir()
	shared := filepath.Join(home, ".agents/skills/example")
	write(t, filepath.Join(shared, "SKILL.md"), "shared")
	write(t, filepath.Join(home, ".codex/skills/same-name/SKILL.md"), "other")
	write(t, filepath.Join(home, ".claude/skills/same-name/SKILL.md"), "third")
	write(t, filepath.Join(home, ".codex/skills/.system/builtin/SKILL.md"), "system")
	write(t, filepath.Join(home, ".claude/skills/synced/group/cloud/SKILL.md"), "cloud")
	if err := os.Symlink(shared, filepath.Join(home, ".codex/skills/example")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(home, ".claude/skills/broken")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".agents/.skill-lock.json"), `{"skills":{"example":{"source":"owner/repo"}}}`)
	inv := Scan(home, []string{filepath.Join(home, ".agents/skills")})
	if len(inv.Items) != 5 {
		t.Fatalf("items: %+v", inv.Items)
	}
	if len(inv.Roots) != 3 {
		t.Fatalf("duplicate root retained: %v", inv.Roots)
	}
	if len(inv.Issues) != 1 || !strings.Contains(inv.Issues[0], "broken") {
		t.Fatalf("issues: %v", inv.Issues)
	}
	var aliases bool
	for _, item := range inv.Items {
		if item.Name == "example" {
			aliases = true
			if len(item.Locations) != 2 || item.Source != "owner/repo" || item.ID != ".agents/skills/example" {
				t.Fatalf("shared item: %+v", item)
			}
		}
		if item.Name == "builtin" && item.OwnerHint != "codex-system" {
			t.Fatalf("system owner: %+v", item)
		}
		if item.Name == "cloud" && item.OwnerHint != "claude-sync" {
			t.Fatalf("synced owner: %+v", item)
		}
	}
	if !aliases {
		t.Fatal("missing shared item")
	}
}

func TestScanReportsInvalidSourceEvidenceAndCycles(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".agents/.skill-lock.json"), "invalid")
	root := filepath.Join(home, ".agents/skills")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "cycle")); err != nil {
		t.Fatal(err)
	}
	inv := Scan(home, nil)
	if len(inv.Issues) != 2 {
		t.Fatalf("issues: %v", inv.Issues)
	}
}

func TestReadTreeTracksAllFilesModesAndSafeLinks(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "SKILL.md"), "instructions\n")
	write(t, filepath.Join(root, "scripts/helper.sh"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(root, "scripts/helper.sh"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("scripts/helper.sh", filepath.Join(root, "helper")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, ".git/config"), "ignored")
	tree, err := ReadTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 3 || tree["scripts/helper.sh"].Mode != 0755 || tree["helper"].Link != "scripts/helper.sh" {
		t.Fatalf("tree: %+v", tree)
	}
	outside := filepath.Join(t.TempDir(), "private")
	write(t, outside, "do not read")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTree(root); err == nil {
		t.Fatal("escaped root")
	}
}

func TestStoreRoundTripAndIntegrity(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	tree := Tree{"SKILL.md": {Data: []byte("instructions\n"), Mode: 0644}, "scripts/x": {Data: []byte{0, 1, 2}, Mode: 0755}, "alias": {Link: "SKILL.md", Mode: 0777}}
	id, err := store.Save(tree)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := store.Save(tree)
	if err != nil || id != id2 {
		t.Fatalf("unstable snapshot: %s %s %v", id, id2, err)
	}
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, tree) {
		t.Fatalf("roundtrip: %+v", loaded)
	}
	write(t, filepath.Join(root, "objects", digest(tree["SKILL.md"].Data)), "corrupt")
	if _, err := store.Load(id); err == nil {
		t.Fatal("accepted corrupt blob")
	}
	if _, err := store.Save(tree); err == nil {
		t.Fatal("overwrote corrupt immutable blob")
	}
	if _, err := store.Load("../../escape"); err == nil {
		t.Fatal("accepted invalid snapshot ID")
	}
}

func TestStoreRejectsUnsafeTreesAndManifest(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, tree := range []Tree{
		{"../escape": {Data: []byte("x")}},
		{"link": {Link: "../../escape"}},
		{"link": {Link: "x"}, "link/child": {Data: []byte("x")}},
		{"x": {Mode: 010000}},
	} {
		if _, err := store.Save(tree); err == nil {
			t.Fatalf("accepted unsafe tree: %+v", tree)
		}
	}
	manifest := snapshot{Version: 1, Files: []storedFile{{Path: "../../escape", Hash: digest(nil)}}}
	data, _ := json.Marshal(manifest)
	id := digest(data)
	write(t, filepath.Join(store.root, "snapshots", id+".json"), string(data))
	if _, err := store.Load(id); err == nil {
		t.Fatal("accepted unsafe manifest")
	}
}

func TestStoreRejectsSymlinkBlob(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	tree := Tree{"file": {Data: []byte("data"), Mode: 0644}}
	id, err := store.Save(tree)
	if err != nil {
		t.Fatal(err)
	}
	object := filepath.Join(root, "objects", digest([]byte("data")))
	if err := os.Remove(object); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	write(t, outside, "data")
	if err := os.Symlink(outside, object); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(id); err == nil {
		t.Fatal("followed store symlink")
	}
}

func TestDiffIncludesAddedRemovedAuxiliaryAndPermissions(t *testing.T) {
	before := Tree{"SKILL.md": {Data: []byte("same\n"), Mode: 0644}, "refs/detail.md": {Data: []byte("old\n"), Mode: 0644}, "removed": {Data: []byte("bye"), Mode: 0644}, "run": {Data: []byte("same"), Mode: 0644}}
	after := Tree{"SKILL.md": {Data: []byte("same\n"), Mode: 0644}, "refs/detail.md": {Data: []byte("new\n"), Mode: 0644}, "added": {Data: []byte{0, 1}, Mode: 0644}, "run": {Data: []byte("same"), Mode: 0755}}
	changes := Diff(before, after)
	if len(changes) != 4 {
		t.Fatalf("changes: %+v", changes)
	}
	if changes[0].Kind != "added" || !changes[0].Binary {
		t.Fatalf("addition: %+v", changes[0])
	}
	if !strings.Contains(changes[1].Diff, "-old\n+new\n") {
		t.Fatalf("content diff: %s", changes[1].Diff)
	}
	if changes[2].Kind != "deleted" {
		t.Fatalf("deletion: %+v", changes[2])
	}
	if !strings.Contains(changes[3].Diff, "0644 -> 0755") {
		t.Fatalf("mode diff: %+v", changes[3])
	}
	if len(Diff(before, before)) != 0 {
		t.Fatal("equal tree differs")
	}
}

func TestTextDiffNoFinalNewlineAndBounds(t *testing.T) {
	change := Diff(Tree{"x": {Data: []byte("old")}}, Tree{"x": {Data: []byte("new\n")}})[0]
	if !strings.Contains(change.Diff, "No newline at end of file") {
		t.Fatal(change.Diff)
	}
	change = Diff(nil, Tree{"x": {Data: []byte(strings.Repeat("x", 128<<10+1))}})[0]
	if !strings.Contains(change.Diff, "omitted") {
		t.Fatal("unbounded diff")
	}
}

func TestConcurrentSnapshotPublication(t *testing.T) {
	store := NewStore(t.TempDir())
	tree := Tree{"SKILL.md": {Data: []byte("same content\n"), Mode: 0644}}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := store.Save(tree)
			if err != nil {
				errs <- err
			} else {
				ids <- id
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("same content produced different IDs")
		}
	}
	loaded, err := store.Load(first)
	if err != nil || !reflect.DeepEqual(loaded, tree) {
		t.Fatalf("incomplete publication: %v", err)
	}
}

func TestStoreRejectsRedirectedDirectory(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	id, err := store.Save(Tree{"x": {Data: []byte("x"), Mode: 0644}})
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "objects")
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, original); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(id); err == nil {
		t.Fatal("followed redirected object directory")
	}
	if _, err := store.Save(Tree{"x": {Data: []byte("x"), Mode: 0644}}); err == nil {
		t.Fatal("wrote through redirected object directory")
	}
}

func TestReadTreeRejectsBrokenAndRelativeEscapingLinks(t *testing.T) {
	for _, target := range []string{"missing", "../outside"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			write(t, filepath.Join(root, "SKILL.md"), "skill")
			if err := os.Symlink(target, filepath.Join(root, "bad")); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadTree(root); err == nil {
				t.Fatal("accepted invalid link")
			}
		})
	}
}
