package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jdevera/dotagents/internal/plugins"
	"github.com/jdevera/dotagents/internal/skills"
)

type Plan struct {
	Revision string   `json:"revision"`
	Actions  []Action `json:"actions"`
	Issues   []string `json:"issues"`
}

type Action struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Owner    string `json:"owner"`
	Snapshot string `json:"snapshot"`
}

type observation struct {
	Path        string `json:"path"`
	Parent      string `json:"parent"`
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
	Tree        string `json:"tree"`
}

// Plan never changes live paths. Its revision includes the catalog and observed
// contents, so a preview cannot silently authorize a later, different change.
func (a *App) Plan() (Plan, error) {
	p := Plan{Actions: []Action{}, Issues: []string{}}
	if err := a.Config.Validate(); err != nil {
		return p, err
	}
	observed := []observation{}
	claimed := map[string]string{}
	selected := append([]string{}, a.Only...)
	sort.Strings(selected)
	for _, id := range selected {
		found := false
		for _, entry := range a.Config.Skills {
			if entry.ID == id {
				found = true
				break
			}
		}
		if !found {
			p.Issues = append(p.Issues, "selected skill is not in the catalog: "+id)
		}
	}
	for _, entry := range a.Config.Skills {
		if len(selected) > 0 && !contains(selected, entry.ID) {
			continue
		}
		wanted := a.skillWanted(entry)
		accepted, err := a.Store().Load(entry.Snapshot)
		if err != nil {
			p.Issues = append(p.Issues, fmt.Sprintf("%s: accepted snapshot unavailable: %v", entry.ID, err))
			continue
		}
		if _, ok := accepted["SKILL.md"]; !ok {
			p.Issues = append(p.Issues, entry.ID+": accepted snapshot has no SKILL.md")
			continue
		}
		for _, location := range entry.Locations {
			path := filepath.Join(a.Home, location)
			parent, err := safeDestination(a.Home, path)
			if err != nil {
				p.Issues = append(p.Issues, fmt.Sprintf("%s: %v", entry.ID, err))
				continue
			}
			physical := filepath.Join(parent, filepath.Base(path))
			for other, owner := range claimed {
				if pathWithin(other, physical) || pathWithin(physical, other) {
					p.Issues = append(p.Issues, fmt.Sprintf("overlapping destinations for %s and %s: %s", entry.ID, owner, path))
				}
			}
			claimed[physical] = entry.ID
			kind, fingerprint, err := pathFingerprint(path)
			if err != nil {
				p.Issues = append(p.Issues, fmt.Sprintf("inspect %s: %v", path, err))
				continue
			}
			ob := observation{Path: path, Parent: parent, Kind: kind, Fingerprint: fingerprint}
			var installed skills.Tree
			if kind != "absent" {
				installed, err = skills.ReadTree(path)
				if err != nil {
					p.Issues = append(p.Issues, fmt.Sprintf("cannot inspect %s: %v", path, err))
					observed = append(observed, ob)
					continue
				}
				if _, ok := installed["SKILL.md"]; !ok {
					p.Issues = append(p.Issues, "refusing to replace non-skill directory: "+path)
					observed = append(observed, ob)
					continue
				}
				ob.Tree = treeFingerprint(installed)
			}
			observed = append(observed, ob)
			if !wanted {
				if kind == "absent" {
					continue
				}
				if entry.Owner == "native" {
					p.Issues = append(p.Issues, entry.ID+": native-owned skill is outside its selected scope; its agent must manage removal")
					continue
				}
				if len(skills.Diff(accepted, installed)) > 0 {
					p.Issues = append(p.Issues, "refusing to remove edited skill outside its selected scope: "+path)
					continue
				}
				p.Actions = append(p.Actions, Action{ID: entry.ID, Path: path, Kind: "remove", Owner: entry.Owner, Snapshot: entry.Snapshot})
				continue
			}
			changed := kind == "absent" || len(skills.Diff(accepted, installed)) > 0
			linkNeeded := entry.Owner == "skillshare" && !a.isManagedLink(path)
			if !changed && !linkNeeded {
				continue
			}
			if entry.Owner == "native" {
				p.Issues = append(p.Issues, entry.ID+": native-owned skill needs attention; its agent must manage installation")
				continue
			}
			action := Action{ID: entry.ID, Path: path, Kind: "replace", Owner: entry.Owner, Snapshot: entry.Snapshot}
			if kind == "absent" {
				action.Kind = "install"
			} else if linkNeeded {
				action.Kind = "link"
			}
			p.Actions = append(p.Actions, action)
		}
	}
	a.checkRemovalDependencies(&p)
	sort.Slice(p.Actions, func(i, j int) bool { return p.Actions[i].Path < p.Actions[j].Path })
	sort.Slice(observed, func(i, j int) bool { return observed[i].Path < observed[j].Path })
	sort.Strings(p.Issues)
	data, err := json.Marshal(struct {
		Config   Config
		Machine  Machine
		Only     []string
		Observed []observation
		Actions  []Action
		Issues   []string
	}{a.Config, a.Machine, selected, observed, p.Actions, p.Issues})
	if err != nil {
		return p, err
	}
	p.Revision = hashBytes(data)
	return p, nil
}

func (a *App) isManagedLink(path string) bool {
	link, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}
	base := filepath.Join(a.Repo, ".dotagents", "transactions")
	rel, err := filepath.Rel(base, filepath.Clean(link))
	if err != nil || !pathWithin(base, link) {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 3 && identifier.MatchString(parts[0]) && parts[1] == "library" && identifier.MatchString(parts[2])
}

// Removing a source directory must not strand an untracked or still-selected
// alias. Every discovered alias depending on it needs its own planned action.
func (a *App) checkRemovalDependencies(plan *Plan) {
	removed := []string{}
	planned := map[string]bool{}
	for _, action := range plan.Actions {
		node, err := canonicalNode(action.Path)
		if err != nil {
			continue
		}
		planned[node] = true
		if action.Kind == "remove" {
			removed = append(removed, node)
		}
	}
	if len(removed) == 0 {
		return
	}
	roots := []string{}
	for _, root := range a.Config.Roots {
		roots = append(roots, expandHome(a.Home, root))
	}
	native := plugins.Scan(a.Home)
	roots = append(roots, native.SkillRoots...)
	inventory := skills.Scan(a.Home, roots)
	inventory.Issues = append(inventory.Issues, native.Issues...)
	for _, issue := range inventory.Issues {
		plan.Issues = append(plan.Issues, "cannot verify skill aliases before removal: "+issue)
	}
	for _, item := range inventory.Items {
		for _, location := range item.Locations {
			node, err := canonicalNode(location)
			if err != nil {
				plan.Issues = append(plan.Issues, "cannot verify alias before removal: "+location)
				continue
			}
			if planned[node] {
				continue
			}
			for depth := 0; depth < 64; depth++ {
				for _, source := range removed {
					if pathWithin(source, node) {
						plan.Issues = append(plan.Issues, fmt.Sprintf("removing %s would break an unchanged skill location: %s", source, location))
						depth = 64
						break
					}
				}
				if depth == 64 {
					break
				}
				link, err := os.Readlink(node)
				if err != nil {
					break
				}
				if !filepath.IsAbs(link) {
					link = filepath.Join(filepath.Dir(node), link)
				}
				node, err = canonicalNode(filepath.Clean(link))
				if err != nil {
					plan.Issues = append(plan.Issues, "cannot verify alias chain before removal: "+location)
					break
				}
			}
		}
	}
}

func canonicalNode(path string) (string, error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

type transaction struct {
	Version   int                `json:"version"`
	ID        string             `json:"id"`
	Status    string             `json:"status"`
	Revision  string             `json:"revision"`
	CreatedAt string             `json:"created_at"`
	Home      string             `json:"home"`
	Repo      string             `json:"repo"`
	Entries   []transactionEntry `json:"entries"`
}

type transactionEntry struct {
	Action              Action `json:"action"`
	OriginalKind        string `json:"original_kind"`
	OriginalFingerprint string `json:"original_fingerprint"`
	OriginalSnapshot    string `json:"original_snapshot,omitempty"`
	Backup              string `json:"backup,omitempty"`
	Stage               string `json:"stage"`
	AppliedFingerprint  string `json:"applied_fingerprint"`
	AppliedLink         string `json:"applied_link,omitempty"`
	TempOld             string `json:"temp_old"`
	TempNew             string `json:"temp_new"`
}

// Apply stages and verifies all outputs and backups before changing a live path.
// Skillshare is only asked to link accepted staged contents into isolated targets.
func (a *App) Apply(ctx context.Context, revision string) (string, error) {
	if revision == "" {
		return "", errors.New("apply requires the revision from a fresh plan")
	}
	unlock, err := a.operationLock()
	if err != nil {
		return "", err
	}
	defer unlock()
	fresh, err := a.Reload()
	if err != nil {
		return "", err
	}
	plan, err := fresh.Plan()
	if err != nil {
		return "", err
	}
	if plan.Revision != revision {
		return "", errors.New("plan is stale; run plan again before applying")
	}
	if len(plan.Issues) > 0 {
		return "", fmt.Errorf("plan has %d unresolved issues; no live paths changed", len(plan.Issues))
	}
	if len(plan.Actions) == 0 {
		return "", nil
	}
	id, err := newTransactionID()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(a.Repo, ".dotagents", "transactions", id)
	if err = ensureRealDirectory(filepath.Dir(dir)); err != nil {
		return "", err
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	record := transaction{Version: 1, ID: id, Status: "preparing", Revision: revision, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Home: a.Home, Repo: a.Repo, Entries: []transactionEntry{}}
	if err = fresh.prepareTransaction(ctx, dir, plan, &record); err != nil {
		return id, fmt.Errorf("preparation failed; live paths unchanged: %w", err)
	}
	if err = writeTransaction(dir, &record); err != nil {
		return id, err
	}
	again, err := a.Reload()
	if err != nil {
		return id, err
	}
	current, err := again.Plan()
	if err != nil {
		return id, err
	}
	if current.Revision != revision {
		return id, errors.New("live state changed during preparation; no live paths changed")
	}
	record.Status = "applying"
	if err = writeTransaction(dir, &record); err != nil {
		return id, err
	}
	for _, entry := range record.Entries {
		if err = ctx.Err(); err == nil {
			err = publishEntry(a.Home, dir, entry)
		}
		if err != nil {
			rollbackErr := a.rollbackTransaction(dir, &record)
			if rollbackErr != nil {
				return id, fmt.Errorf("apply failed: %v; automatic rollback could not complete: %w; backups retained at %s", err, rollbackErr, dir)
			}
			return id, fmt.Errorf("apply failed and original targets were restored: %w", err)
		}
	}
	record.Status = "applied"
	if err = writeTransaction(dir, &record); err != nil {
		return id, fmt.Errorf("targets applied but recording completion failed; backups retained at %s: %w", dir, err)
	}
	for _, entry := range record.Entries {
		if entry.OriginalKind != "absent" {
			if err = removeIfFingerprint(entry.TempOld, entry.OriginalFingerprint); err != nil {
				return id, fmt.Errorf("applied; temporary original retained: %w", err)
			}
		}
	}
	return id, nil
}

func (a *App) prepareTransaction(ctx context.Context, dir string, plan Plan, record *transaction) error {
	evidence := skills.NewStore(filepath.Join(dir, "evidence"))
	for i, action := range plan.Actions {
		if err := ctx.Err(); err != nil {
			return err
		}
		kind, fingerprint, err := pathFingerprint(action.Path)
		if err != nil {
			return err
		}
		entry := transactionEntry{Action: action, OriginalKind: kind, OriginalFingerprint: fingerprint, Stage: filepath.Join("staged", fmt.Sprintf("%04d", i)), TempOld: filepath.Join(filepath.Dir(action.Path), ".dotagents-old-"+record.ID+fmt.Sprintf("-%04d", i)), TempNew: filepath.Join(filepath.Dir(action.Path), ".dotagents-new-"+record.ID+fmt.Sprintf("-%04d", i))}
		for _, temporary := range []string{entry.TempOld, entry.TempNew} {
			if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("temporary path already exists or cannot be checked: %s", temporary)
			}
		}
		if kind != "absent" {
			entry.Backup = filepath.Join("backups", fmt.Sprintf("%04d", i))
			if err = copyNode(action.Path, filepath.Join(dir, entry.Backup)); err != nil {
				return err
			}
			_, backupPrint, err := pathFingerprint(filepath.Join(dir, entry.Backup))
			if err != nil || backupPrint != fingerprint {
				return fmt.Errorf("backup verification failed for %s: %v", action.Path, err)
			}
			tree, err := skills.ReadTree(action.Path)
			if err != nil {
				return err
			}
			entry.OriginalSnapshot, err = evidence.Save(tree)
			if err != nil {
				return err
			}
		}
		accepted, err := a.Store().Load(action.Snapshot)
		if err != nil {
			return err
		}
		if action.Kind == "remove" {
			record.Entries = append(record.Entries, entry)
			continue
		}
		if action.Owner == "skillshare" {
			library := filepath.Join(dir, "library", action.ID)
			if _, err := os.Lstat(library); errors.Is(err, os.ErrNotExist) {
				if err = writeTree(library, accepted); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			entry.AppliedLink = library
		} else if err = writeTree(filepath.Join(dir, entry.Stage), accepted); err != nil {
			return err
		}
		record.Entries = append(record.Entries, entry)
	}
	if err := stageSkillshare(ctx, dir, record.Entries); err != nil {
		return err
	}
	for i := range record.Entries {
		entry := &record.Entries[i]
		if entry.Action.Kind == "remove" {
			continue
		}
		stage := filepath.Join(dir, entry.Stage)
		actual, err := skills.ReadTree(stage)
		if err != nil {
			return err
		}
		accepted, err := a.Store().Load(entry.Action.Snapshot)
		if err != nil {
			return err
		}
		if len(skills.Diff(accepted, actual)) > 0 {
			return fmt.Errorf("staged contents differ from accepted snapshot: %s", entry.Action.ID)
		}
		_, entry.AppliedFingerprint, err = pathFingerprint(stage)
		if err != nil {
			return err
		}
	}
	record.Status = "prepared"
	return nil
}

func stageSkillshare(ctx context.Context, dir string, entries []transactionEntry) error {
	targets := map[string]any{}
	for i, entry := range entries {
		if entry.Action.Owner == "skillshare" && entry.Action.Kind != "remove" {
			path := filepath.Join(dir, "skillshare-targets", fmt.Sprintf("%04d", i))
			if err := os.MkdirAll(path, 0700); err != nil {
				return err
			}
			targets[fmt.Sprintf("target-%04d", i)] = map[string]any{"skills": map[string]any{"path": path, "include": []string{entry.Action.ID}}}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	binary, err := exec.LookPath("skillshare")
	if err != nil {
		return errors.New("Skillshare is required to apply Skillshare-owned entries; install it first")
	}
	config := map[string]any{"sources": map[string]string{"skills": filepath.Join(dir, "library"), "agents": filepath.Join(dir, "agents")}, "mode": "merge", "targets": targets}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(dir, "agents"), 0700); err != nil {
		return err
	}
	configPath := filepath.Join(dir, "skillshare-config.yaml")
	if err = os.WriteFile(configPath, data, 0600); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, binary, "sync", "--global", "--no-tui")
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = dir
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "SKILLSHARE_") && key != "XDG_CONFIG_HOME" && key != "XDG_DATA_HOME" && key != "XDG_STATE_HOME" && key != "XDG_CACHE_HOME" {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "SKILLSHARE_CONFIG="+configPath, "XDG_CONFIG_HOME="+filepath.Join(dir, "runtime", "config"), "XDG_DATA_HOME="+filepath.Join(dir, "runtime", "data"), "XDG_STATE_HOME="+filepath.Join(dir, "runtime", "state"), "XDG_CACHE_HOME="+filepath.Join(dir, "runtime", "cache"))
	var output limitedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("isolated Skillshare sync failed: %w: %s", err, output.String())
	}
	for i, entry := range entries {
		if entry.Action.Owner == "skillshare" && entry.Action.Kind != "remove" {
			generated := filepath.Join(dir, "skillshare-targets", fmt.Sprintf("%04d", i), entry.Action.ID)
			link, err := os.Readlink(generated)
			if err != nil {
				return fmt.Errorf("Skillshare did not produce a link for %s: %w", entry.Action.ID, err)
			}
			resolved, err := filepath.EvalSymlinks(generated)
			if err != nil {
				return err
			}
			expected, err := filepath.EvalSymlinks(entry.AppliedLink)
			if err != nil {
				return err
			}
			if link == "" || resolved != expected {
				return fmt.Errorf("Skillshare linked %s to an unexpected source", entry.Action.ID)
			}
			stage := filepath.Join(dir, entry.Stage)
			if err = os.MkdirAll(filepath.Dir(stage), 0700); err != nil {
				return err
			}
			if err = os.Symlink(entry.AppliedLink, stage); err != nil {
				return err
			}
		}
	}
	return nil
}

type limitedOutput struct{ buffer bytes.Buffer }

func (b *limitedOutput) String() string { return b.buffer.String() }

func (b *limitedOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := (64 << 10) - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return n, nil
}

func renameNode(source, destination string) error {
	return renameWithDirectoryPermissions(source, destination, os.Rename)
}

func renameWithDirectoryPermissions(source, destination string, rename func(string, string) error) error {
	err := rename(source, destination)
	if !errors.Is(err, fs.ErrPermission) {
		return err
	}
	info, statErr := os.Lstat(source)
	if statErr != nil || !info.IsDir() || info.Mode().Perm()&0200 != 0 {
		return err
	}
	// Some Darwin filesystems require write permission on the directory being
	// renamed. Keep a descriptor so restoring its mode cannot follow a replaced
	// source path or a symbolic link, including after a successful rename.
	directory, openErr := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if openErr != nil {
		return errors.Join(err, openErr)
	}
	defer directory.Close()
	opened, statErr := directory.Stat()
	if statErr != nil {
		return errors.Join(err, statErr)
	}
	if !os.SameFile(info, opened) {
		return fmt.Errorf("directory changed before rename: %s", source)
	}
	if chmodErr := directory.Chmod(info.Mode() | 0200); chmodErr != nil {
		return errors.Join(err, chmodErr)
	}
	renameErr := rename(source, destination)
	restoreErr := directory.Chmod(info.Mode())
	if restoreErr != nil {
		restoreErr = fmt.Errorf("restore directory permissions after rename of %s: %w", source, restoreErr)
	}
	return errors.Join(renameErr, restoreErr)
}

func publishEntry(home, dir string, entry transactionEntry) error {
	if _, err := safeDestination(home, entry.Action.Path); err != nil {
		return err
	}
	kind, fingerprint, err := pathFingerprint(entry.Action.Path)
	if err != nil {
		return err
	}
	if kind != entry.OriginalKind || fingerprint != entry.OriginalFingerprint {
		return fmt.Errorf("target changed after preview: %s", entry.Action.Path)
	}
	if kind == "dir" {
		current, err := skills.ReadTree(entry.Action.Path)
		if err != nil {
			return err
		}
		original, err := skills.NewStore(filepath.Join(dir, "evidence")).Load(entry.OriginalSnapshot)
		if err != nil {
			return err
		}
		if len(skills.Diff(original, current)) > 0 {
			return fmt.Errorf("target contents changed after backup: %s", entry.Action.Path)
		}
	}
	if err = os.MkdirAll(filepath.Dir(entry.Action.Path), 0755); err != nil {
		return err
	}
	if _, err = safeDestination(home, entry.Action.Path); err != nil {
		return err
	}
	if entry.AppliedLink == "" && entry.Action.Kind != "remove" {
		if err = copyNode(filepath.Join(dir, entry.Stage), entry.TempNew); err != nil {
			return err
		}
	}
	if kind != "absent" {
		if err = renameNode(entry.Action.Path, entry.TempOld); err != nil {
			return err
		}
	}
	if entry.Action.Kind == "remove" {
		if _, err := os.Lstat(entry.Action.Path); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("removed destination reappeared or cannot be inspected: %s", entry.Action.Path)
		}
		return nil
	}
	if entry.AppliedLink != "" {
		err = os.Symlink(entry.AppliedLink, entry.Action.Path)
	} else {
		if _, checkErr := os.Lstat(entry.Action.Path); !errors.Is(checkErr, os.ErrNotExist) {
			return fmt.Errorf("destination appeared during apply: %s", entry.Action.Path)
		}
		err = renameNode(entry.TempNew, entry.Action.Path)
	}
	if err != nil {
		return err
	}
	_, actual, err := pathFingerprint(entry.Action.Path)
	if err != nil {
		return err
	}
	if actual != entry.AppliedFingerprint {
		return fmt.Errorf("published target verification failed: %s", entry.Action.Path)
	}
	return nil
}

func (a *App) Rollback(id string) error {
	if !identifier.MatchString(id) {
		return errors.New("invalid transaction ID")
	}
	unlock, err := a.operationLock()
	if err != nil {
		return err
	}
	defer unlock()
	dir := filepath.Join(a.Repo, ".dotagents", "transactions", id)
	for _, path := range []string{filepath.Dir(dir), dir} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid transaction directory: %s", path)
		}
	}
	info, err := os.Lstat(filepath.Join(dir, "transaction.json"))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return errors.New("invalid transaction manifest")
	}
	data, err := os.ReadFile(filepath.Join(dir, "transaction.json"))
	if err != nil {
		return err
	}
	var record transaction
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&record); err != nil {
		return err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return errors.New("transaction contains trailing data")
	}
	if record.Version != 1 || record.ID != id || record.Home != a.Home || record.Repo != a.Repo {
		return errors.New("transaction does not belong to this repository and home")
	}
	if record.Status == "rolled_back" {
		return nil
	}
	if record.Status != "prepared" && record.Status != "applying" && record.Status != "applied" {
		return fmt.Errorf("unsupported transaction state %q", record.Status)
	}
	return a.rollbackTransaction(dir, &record)
}

func (a *App) rollbackTransaction(dir string, record *transaction) error {
	type restore struct {
		entry  transactionEntry
		needed bool
	}
	operations := []restore{}
	claimed := map[string]bool{}
	for i, entry := range record.Entries {
		if err := validateTransactionEntry(a, dir, record.ID, i, entry); err != nil {
			return err
		}
		parent, err := safeDestination(a.Home, entry.Action.Path)
		if err != nil {
			return err
		}
		physical := filepath.Join(parent, filepath.Base(entry.Action.Path))
		for other := range claimed {
			if pathWithin(other, physical) || pathWithin(physical, other) {
				return errors.New("overlapping transaction destinations")
			}
		}
		claimed[physical] = true
		if entry.OriginalKind != "absent" {
			info, err := os.Lstat(filepath.Join(dir, "backups"))
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("invalid backup directory")
			}
			kind, print, err := pathFingerprint(filepath.Join(dir, entry.Backup))
			if err != nil || print != entry.OriginalFingerprint || kind != entry.OriginalKind {
				return fmt.Errorf("backup damaged for %s: %v", entry.Action.Path, err)
			}
		}
		kind, print, err := pathFingerprint(entry.Action.Path)
		if err != nil {
			return err
		}
		if kind == entry.OriginalKind && print == entry.OriginalFingerprint {
			operations = append(operations, restore{entry, false})
			continue
		}
		if kind != "absent" && print != entry.AppliedFingerprint {
			return fmt.Errorf("refusing rollback: %s changed since apply", entry.Action.Path)
		}
		if kind != "absent" {
			actual, err := skills.ReadTree(entry.Action.Path)
			if err != nil {
				return err
			}
			expected, err := a.Store().Load(entry.Action.Snapshot)
			if err != nil {
				return err
			}
			if len(skills.Diff(expected, actual)) > 0 {
				return fmt.Errorf("refusing rollback: %s contents changed since apply", entry.Action.Path)
			}
		} else if record.Status == "applied" && entry.Action.Kind != "remove" {
			return fmt.Errorf("refusing rollback: %s was removed since apply", entry.Action.Path)
		}
		operations = append(operations, restore{entry, true})
	}
	for i := len(operations) - 1; i >= 0; i-- {
		op := operations[i]
		entry := op.entry
		if !op.needed {
			continue
		}
		if _, err := safeDestination(a.Home, entry.Action.Path); err != nil {
			return err
		}
		if entry.OriginalKind != "absent" {
			if _, err := os.Lstat(entry.TempNew); errors.Is(err, os.ErrNotExist) {
				if err = copyNode(filepath.Join(dir, entry.Backup), entry.TempNew); err != nil {
					return err
				}
			} else {
				_, print, e := pathFingerprint(entry.TempNew)
				if e != nil {
					return e
				}
				if print != entry.OriginalFingerprint {
					if print != entry.AppliedFingerprint {
						return fmt.Errorf("unexpected recovery staging contents: %s", entry.TempNew)
					}
					if err = os.RemoveAll(entry.TempNew); err != nil {
						return err
					}
					if err = copyNode(filepath.Join(dir, entry.Backup), entry.TempNew); err != nil {
						return err
					}
				}
			}
		}
		kind, print, err := pathFingerprint(entry.Action.Path)
		if err != nil {
			return err
		}
		if kind != "absent" {
			if print != entry.AppliedFingerprint {
				return fmt.Errorf("target changed during rollback: %s", entry.Action.Path)
			}
			retired := filepath.Join(filepath.Dir(entry.Action.Path), ".dotagents-retired-"+record.ID+fmt.Sprintf("-%04d", i))
			if _, err = os.Lstat(retired); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("recovery path already exists: %s", retired)
			}
			if err = renameNode(entry.Action.Path, retired); err != nil {
				return err
			}
			if entry.OriginalKind != "absent" {
				if err = renameNode(entry.TempNew, entry.Action.Path); err != nil {
					_ = renameNode(retired, entry.Action.Path)
					return err
				}
			}
			if err = removeIfFingerprint(retired, entry.AppliedFingerprint); err != nil {
				return err
			}
		} else if entry.OriginalKind != "absent" {
			if err = renameNode(entry.TempNew, entry.Action.Path); err != nil {
				return err
			}
		}
	}
	for _, entry := range record.Entries {
		if entry.OriginalKind != "absent" {
			if err := removeIfFingerprint(entry.TempOld, entry.OriginalFingerprint); err != nil {
				return err
			}
		}
		if _, err := os.Lstat(entry.TempNew); err == nil {
			_, print, e := pathFingerprint(entry.TempNew)
			if e != nil {
				return e
			}
			if print != entry.AppliedFingerprint && print != entry.OriginalFingerprint {
				return fmt.Errorf("unexpected temporary contents retained: %s", entry.TempNew)
			}
			if err = os.RemoveAll(entry.TempNew); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	record.Status = "rolled_back"
	return writeTransaction(dir, record)
}

func validateTransactionEntry(a *App, dir, id string, index int, entry transactionEntry) error {
	if _, err := safeDestination(a.Home, entry.Action.Path); err != nil {
		return err
	}
	rel, err := filepath.Rel(a.Home, entry.Action.Path)
	if err != nil {
		return err
	}
	if err = validateLocation(rel); err != nil {
		return err
	}
	if entry.Stage != filepath.Join("staged", fmt.Sprintf("%04d", index)) {
		return errors.New("invalid transaction staging path")
	}
	if entry.OriginalKind != "absent" && entry.OriginalKind != "dir" && entry.OriginalKind != "link" {
		return errors.New("invalid original target kind")
	}
	if entry.OriginalKind != "absent" && entry.Backup != filepath.Join("backups", fmt.Sprintf("%04d", index)) {
		return errors.New("invalid backup path")
	}
	if !identifier.MatchString(entry.Action.ID) {
		return errors.New("invalid transaction skill ID")
	}
	switch entry.Action.Kind {
	case "install", "replace", "link":
		if entry.AppliedFingerprint == "" {
			return errors.New("missing applied target fingerprint")
		}
	case "remove":
		if entry.OriginalKind == "absent" || entry.AppliedFingerprint != "" || entry.AppliedLink != "" {
			return errors.New("invalid removal transaction")
		}
	default:
		return errors.New("invalid transaction action")
	}
	if entry.AppliedLink != "" && entry.AppliedLink != filepath.Join(dir, "library", entry.Action.ID) {
		return errors.New("invalid managed link destination")
	}
	if entry.TempOld != filepath.Join(filepath.Dir(entry.Action.Path), ".dotagents-old-"+id+fmt.Sprintf("-%04d", index)) || entry.TempNew != filepath.Join(filepath.Dir(entry.Action.Path), ".dotagents-new-"+id+fmt.Sprintf("-%04d", index)) {
		return errors.New("invalid temporary recovery path")
	}
	return nil
}

func writeTransaction(dir string, record *transaction) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "transaction.json"), append(data, '\n'), 0600)
}

func (a *App) operationLock() (func(), error) {
	state := filepath.Join(a.Repo, ".dotagents")
	if err := ensureRealDirectory(state); err != nil {
		return nil, err
	}
	lock := filepath.Join(state, "operation.lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		return nil, fmt.Errorf("cannot acquire operation lock %s (another operation may be running; stale locks require inspection): %w", lock, err)
	}
	return func() { _ = os.Remove(lock) }, nil
}

func ensureRealDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("not a real directory: %s", path)
	}
	return nil
}

func newTransactionID() (string, error) {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(suffix[:]), nil
}

func hashBytes(data []byte) string            { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func treeFingerprint(tree skills.Tree) string { data, _ := json.Marshal(tree); return hashBytes(data) }
func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Resolve every existing parent without following the target itself: replacing
// a link must replace that link, not its destination.
func safeDestination(home, target string) (string, error) {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target || target == home || !pathWithin(home, target) {
		return "", fmt.Errorf("destination is outside home: %s", target)
	}
	rel, err := filepath.Rel(home, target)
	if err != nil {
		return "", err
	}
	if err = validateLocation(rel); err != nil {
		return "", err
	}
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(target)
	cursor := parent
	missing := []string{}
	for {
		_, err := os.Lstat(cursor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		missing = append(missing, filepath.Base(cursor))
		next := filepath.Dir(cursor)
		if next == cursor {
			return "", errors.New("no existing destination ancestor")
		}
		cursor = next
	}
	resolved, err := filepath.EvalSymlinks(cursor)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("destination parent is not a directory: %s", cursor)
	}
	if !pathWithin(realHome, resolved) {
		return "", fmt.Errorf("destination ancestor leaves home: %s", target)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return resolved, nil
}

func writeTree(destination string, tree skills.Tree) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0755); err != nil {
		return err
	}
	names := make([]string, 0, len(tree))
	for name := range tree {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !fs.ValidPath(name) || name == "." || strings.Contains(name, "\\") {
			return errors.New("invalid snapshot file path")
		}
		file := tree[name]
		path := filepath.Join(destination, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if file.Link != "" {
			if err := os.Symlink(file.Link, path); err != nil {
				return err
			}
		} else {
			if err := os.WriteFile(path, file.Data, os.FileMode(file.Mode)); err != nil {
				return err
			}
			if err := os.Chmod(path, os.FileMode(file.Mode)); err != nil {
				return err
			}
		}
	}
	return nil
}

// pathFingerprint covers complete original nodes, including directory modes,
// .git files, and link strings. It never follows links while making a backup.
func pathFingerprint(path string) (string, string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", "", nil
	}
	if err != nil {
		return "", "", err
	}
	kind := "dir"
	if info.Mode()&os.ModeSymlink != 0 {
		kind = "link"
	} else if !info.IsDir() {
		return "", "", fmt.Errorf("target is neither a skill directory nor a link: %s", path)
	}
	h := sha256.New()
	count := 0
	var total int64
	err = filepath.WalkDir(path, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > skills.MaxTreeFiles*2 {
			return errors.New("backup entry count exceeds limit")
		}
		rel, err := filepath.Rel(path, filename)
		if err != nil {
			return err
		}
		if strings.Count(rel, string(filepath.Separator)) > 64 {
			return errors.New("backup depth exceeds limit")
		}
		info, err := os.Lstat(filename)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%q\x00%d\x00", filepath.ToSlash(rel), uint32(info.Mode()))
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(filename)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%q\x00", link)
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported backup file type: %s", filename)
		}
		if info.Size() > skills.MaxFileBytes {
			return errors.New("backup file exceeds size limit")
		}
		file, err := os.Open(filename)
		if err != nil {
			return err
		}
		read, err := io.Copy(h, io.LimitReader(file, skills.MaxFileBytes+1))
		file.Close()
		if err != nil {
			return err
		}
		total += read
		if read > skills.MaxFileBytes || total > skills.MaxTreeBytes {
			return errors.New("backup byte count exceeds limit")
		}
		h.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return kind, hex.EncodeToString(h.Sum(nil)), nil
}

func copyNode(source, destination string) error {
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("backup/staging destination already exists or cannot be inspected: %s", destination)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	type directoryMode struct {
		path string
		mode os.FileMode
	}
	directories := []directoryMode{}
	count := 0
	var total int64
	err := filepath.WalkDir(source, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > skills.MaxTreeFiles*2 {
			return errors.New("copy entry count exceeds limit")
		}
		rel, err := filepath.Rel(source, filename)
		if err != nil {
			return err
		}
		if strings.Count(rel, string(filepath.Separator)) > 64 {
			return errors.New("copy depth exceeds limit")
		}
		target := filepath.Join(destination, rel)
		info, err := os.Lstat(filename)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(filename)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if info.IsDir() {
			directories = append(directories, directoryMode{target, info.Mode().Perm()})
			return os.Mkdir(target, info.Mode().Perm()|0700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported backup file type: %s", filename)
		}
		input, err := os.Open(filename)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		n, err := io.Copy(output, io.LimitReader(input, skills.MaxFileBytes+1))
		total += n
		if err == nil && (n > skills.MaxFileBytes || total > skills.MaxTreeBytes) {
			err = errors.New("copy byte count exceeds limit")
		}
		if err == nil {
			err = output.Sync()
		}
		closeErr := output.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return os.Chmod(target, info.Mode().Perm())
	})
	if err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err = os.Chmod(directories[i].path, directories[i].mode); err != nil {
			return err
		}
	}
	return nil
}

func removeIfFingerprint(path, expected string) error {
	kind, actual, err := pathFingerprint(path)
	if err != nil {
		return err
	}
	if kind == "absent" {
		return nil
	}
	if actual != expected {
		return fmt.Errorf("refusing to remove changed temporary path: %s", path)
	}
	if kind == "dir" {
		if err := filepath.WalkDir(path, func(filename string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				return os.Chmod(filename, info.Mode().Perm()|0700)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return os.RemoveAll(path)
}
