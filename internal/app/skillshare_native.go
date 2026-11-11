package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jdevera/dotagents/internal/skills"
)

// NativePlan describes the handoff to the ordinary Skillshare configuration.
// Preparing its library never accepts new contents into the audit catalog.
type NativePlan struct {
	Revision    string            `json:"revision"`
	ConfigPath  string            `json:"config_path"`
	Source      string            `json:"source"`
	Initialized bool              `json:"initialized"`
	Imports     []NativeImport    `json:"imports"`
	Targets     []NativeTarget    `json:"targets"`
	Collisions  []NativeCollision `json:"collisions"`
	Issues      []string          `json:"issues"`
	Warnings    []string          `json:"warnings"`
	Pending     []string          `json:"pending_sync"`
	Ready       bool              `json:"ready"`
}
type NativeImport struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Source    string `json:"source"`
	Ref       string `json:"ref,omitempty"`
	Present   bool   `json:"present"`
	Changes   int    `json:"changes"`
	Updatable bool   `json:"updatable"`
}
type NativeTarget struct {
	Name          string   `json:"name"`
	Path          string   `json:"path"`
	Existing      bool     `json:"existing"`
	AddExclude    []string `json:"add_exclude"`
	RemoveExclude []string `json:"remove_exclude"`
	Exclude       []string `json:"exclude"`
	Include       []string `json:"include"`
}
type NativeCollision struct {
	ID              string `json:"id"`
	Path            string `json:"path"`
	Kind            string `json:"kind"`
	MatchesAccepted bool   `json:"matches_accepted"`
	Message         string `json:"message"`
}
type NativeSkillshareResult struct {
	Phase    string     `json:"phase"`
	Plan     NativePlan `json:"plan"`
	Commands []string   `json:"commands"`
	Message  string     `json:"message"`
}
type nativeState struct {
	Version    int                 `json:"version"`
	ConfigPath string              `json:"config_path"`
	Source     string              `json:"source"`
	Excludes   map[string][]string `json:"excludes"`
}
type nativeTargetStatus struct {
	Name          string   `json:"name"`
	Path          string   `json:"path"`
	Mode          string   `json:"mode"`
	Naming        string   `json:"targetNaming"`
	Include       []string `json:"include"`
	Exclude       []string `json:"exclude"`
	SkillsEnabled bool     `json:"skillsEnabled"`
}

func (a *App) SkillshareConfigPath() string {
	actual, _ := os.UserHomeDir()
	if filepath.Clean(actual) == filepath.Clean(a.Home) {
		if value := os.Getenv("SKILLSHARE_CONFIG"); value != "" {
			return expandHome(a.Home, value)
		}
		if value := os.Getenv("XDG_CONFIG_HOME"); value != "" {
			return filepath.Join(value, "skillshare", "config.yaml")
		}
	}
	return filepath.Join(a.Home, ".config", "skillshare", "config.yaml")
}

// SkillshareCommand invokes the normal native manager, with its global config
// selected explicitly. No caller receives implicit install, sync or force flags.
func (a *App) SkillshareCommand(ctx context.Context, args ...string) ([]byte, error) {
	binary, err := exec.LookPath("skillshare")
	if err != nil {
		return nil, errors.New("Skillshare is required; install the skillshare CLI first")
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	args = append(append([]string{}, args...), "--global")
	cmd := exec.CommandContext(bounded, binary, args...)
	cmd.Dir = a.Repo
	cmd.WaitDelay = 2 * time.Second
	replacements := map[string]string{"SKILLSHARE_CONFIG": a.SkillshareConfigPath()}
	actual, _ := os.UserHomeDir()
	if filepath.Clean(actual) != filepath.Clean(a.Home) {
		replacements["XDG_CONFIG_HOME"] = filepath.Join(a.Home, ".config")
		replacements["XDG_DATA_HOME"] = filepath.Join(a.Home, ".local", "share")
		replacements["XDG_STATE_HOME"] = filepath.Join(a.Home, ".local", "state")
		replacements["XDG_CACHE_HOME"] = filepath.Join(a.Home, ".cache")
	}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if _, replace := replacements[key]; !replace && !strings.HasPrefix(key, "SKILLSHARE_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	for key, value := range replacements {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var output nativeOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("Skillshare %s failed: %w: %s", args[0], err, output.String())
	}
	if output.truncated {
		return nil, errors.New("Skillshare output exceeded 1 MiB")
	}
	return []byte(output.String()), nil
}

func (a *App) nativeStatePath() string {
	return filepath.Join(a.Repo, ".dotagents", "skillshare-native.json")
}
func (a *App) readNativeState() (nativeState, error) {
	state := nativeState{Version: 1, Excludes: map[string][]string{}}
	data, err := os.ReadFile(a.nativeStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if len(data) > 1<<20 {
		return state, errors.New("native Skillshare ownership record exceeds 1 MiB")
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Version != 1 {
		return state, errors.New("unsupported native Skillshare ownership record")
	}
	if state.Excludes == nil {
		state.Excludes = map[string][]string{}
	}
	return state, nil
}
func (a *App) writeNativeState(state nativeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(a.nativeStatePath(), append(data, '\n'), 0600)
}

func (a *App) NativeSkillsharePlan(ctx context.Context) (NativePlan, error) {
	plan := NativePlan{ConfigPath: a.SkillshareConfigPath(), Source: filepath.Join(a.Repo, "skills"), Imports: []NativeImport{}, Targets: []NativeTarget{}, Collisions: []NativeCollision{}, Issues: []string{}}
	if a.Config.Scope == "project" {
		return plan, errors.New("connect Skillshare from the home catalog; projects use the native Skillshare project configuration")
	}
	if err := a.Config.Validate(); err != nil {
		return plan, err
	}
	state, err := a.readNativeState()
	if err != nil {
		return plan, err
	}
	var configBytes []byte
	nativeTargets := []nativeTargetStatus{}
	if info, err := os.Lstat(plan.ConfigPath); err == nil {
		if !info.Mode().IsRegular() {
			return plan, fmt.Errorf("native config is not a regular file: %s", plan.ConfigPath)
		}
		if info.Size() > 4<<20 {
			return plan, errors.New("native Skillshare config exceeds 4 MiB")
		}
		configBytes, err = os.ReadFile(plan.ConfigPath)
		if err != nil {
			return plan, err
		}
		data, err := a.SkillshareCommand(ctx, "status", "--json")
		if err != nil {
			return plan, err
		}
		var status struct {
			Source struct {
				Path string `json:"path"`
			} `json:"source"`
		}
		if err = json.Unmarshal(data, &status); err != nil {
			return plan, fmt.Errorf("read native Skillshare status: %w", err)
		}
		if !filepath.IsAbs(status.Source.Path) {
			return plan, errors.New("Skillshare did not report an absolute source path")
		}
		plan.Source = filepath.Clean(status.Source.Path)
		plan.Initialized = true
		data, err = a.SkillshareCommand(ctx, "target", "list", "--json")
		if err != nil {
			return plan, err
		}
		var targets struct {
			Targets []nativeTargetStatus `json:"targets"`
		}
		if err = json.Unmarshal(data, &targets); err != nil {
			return plan, fmt.Errorf("read native Skillshare targets: %w", err)
		}
		nativeTargets = targets.Targets
	} else if !errors.Is(err, os.ErrNotExist) {
		return plan, err
	}
	if state.ConfigPath != "" && (state.ConfigPath != plan.ConfigPath || state.Source != plan.Source) {
		plan.Issues = append(plan.Issues, "native Skillshare config/source changed since this catalog connected; review its ownership record before reconnecting")
	}
	for _, root := range []string{".agents/skills", ".codex/skills", ".claude/skills"} {
		if nativeWithin(filepath.Join(a.Home, root), plan.Source) {
			plan.Issues = append(plan.Issues, "Skillshare source is inside an agent discovery root; move the library outside agent skill directories so filters can control exposure")
		}
	}
	byPath := map[string]*NativeTarget{}
	usedNames := map[string]string{}
	disabledPaths := map[string]string{}
	for _, target := range nativeTargets {
		if !filepath.IsAbs(target.Path) {
			plan.Issues = append(plan.Issues, "native target has no absolute path: "+target.Name)
			continue
		}
		usedNames[target.Name] = filepath.Clean(target.Path)
		if !target.SkillsEnabled {
			disabledPaths[filepath.Clean(target.Path)] = target.Name
			continue
		}
		p := filepath.Clean(target.Path)
		if byPath[p] != nil {
			plan.Issues = append(plan.Issues, "multiple native targets share "+p)
			continue
		}
		byPath[p] = &NativeTarget{Name: target.Name, Path: p, Existing: true, AddExclude: []string{}, RemoveExclude: []string{}, Exclude: append([]string{}, target.Exclude...), Include: append([]string{}, target.Include...)}
		usedNames[target.Name] = p
		if target.Mode != "merge" && target.Mode != "copy" {
			plan.Issues = append(plan.Issues, "native target "+target.Name+" needs merge or copy mode for machine filtering")
		}
		if target.Naming != "" && target.Naming != "flat" {
			plan.Issues = append(plan.Issues, "native target "+target.Name+" uses unsupported naming; select flat names before connecting")
		}
	}
	metadata, metadataErr := nativeMetadata(plan.Source)
	if metadataErr != nil {
		plan.Warnings = append(plan.Warnings, "Native update provenance could not be read: "+metadataErr.Error())
	}
	names := map[string]string{}
	observed := []string{}
	for _, entry := range a.Config.Skills {
		if entry.Owner != "skillshare" {
			continue
		}
		if old := names[entry.Name]; old != "" {
			plan.Issues = append(plan.Issues, fmt.Sprintf("native name %s is claimed by %s and %s", entry.Name, old, entry.ID))
			continue
		}
		names[entry.Name] = entry.ID
		source, ref := nativeUpstream(entry.Source)
		imp := NativeImport{ID: entry.ID, Name: entry.Name, Source: source, Ref: ref}
		if !nativeSource(source) {
			plan.Issues = append(plan.Issues, entry.ID+": upstream must be a GitHub repository source; use Skillshare directly for local or unsupported sources")
		}
		accepted, err := a.Store().Load(entry.Snapshot)
		if err != nil {
			return plan, err
		}
		library := filepath.Join(plan.Source, entry.Name)
		if _, err := os.Lstat(library); err == nil {
			tree, err := skills.ReadTree(library)
			if err != nil {
				plan.Issues = append(plan.Issues, "cannot inspect native source "+library+": "+err.Error())
			} else {
				if marker, ok := tree["SKILL.md"]; !ok || marker.Link != "" || len(marker.Data) == 0 {
					plan.Issues = append(plan.Issues, "native source has no regular nonempty SKILL.md: "+library)
				}
				imp.Present = true
				provenance := metadata[entry.Name]
				imp.Updatable = strings.HasPrefix(provenance.Type, "github") && provenance.Source != "" && nativeRepository(provenance.RepoURL) == nativeRepository(source)
				if !imp.Updatable {
					plan.Warnings = append(plan.Warnings, entry.ID+": native source has no matching upstream metadata; Skillshare can display it, but upstream updates are not verified")
				}
				imp.Changes = len(skills.Diff(accepted, tree))
				observed = append(observed, library+":"+treeFingerprint(tree))
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return plan, err
		}
		plan.Imports = append(plan.Imports, imp)
		for _, location := range entry.Locations {
			absolute := filepath.Join(a.Home, location)
			if _, err := safeDestination(a.Home, absolute); err != nil {
				plan.Issues = append(plan.Issues, entry.ID+": "+err.Error())
				continue
			}
			directory := filepath.Dir(absolute)
			if name := disabledPaths[directory]; name != "" {
				plan.Issues = append(plan.Issues, "native target "+name+" has skill syncing disabled; review that native choice before connecting")
				continue
			}
			if filepath.Base(absolute) != entry.Name {
				plan.Issues = append(plan.Issues, entry.ID+": native target name differs from skill name at "+location)
				continue
			}
			if byPath[directory] == nil {
				name := nativeTargetName(a.Home, directory)
				if usedNames[name] != "" && usedNames[name] != directory {
					plan.Issues = append(plan.Issues, "native target name "+name+" already points elsewhere; configure its desired path manually")
					continue
				}
				usedNames[name] = directory
				byPath[directory] = &NativeTarget{Name: name, Path: directory, AddExclude: []string{}, RemoveExclude: []string{}, Exclude: []string{}, Include: []string{}}
			}
			kind, fingerprint, err := pathFingerprint(absolute)
			if err != nil {
				return plan, err
			}
			observed = append(observed, absolute+":"+kind+":"+fingerprint)
			if kind == "absent" {
				if a.skillWanted(entry) {
					plan.Pending = append(plan.Pending, absolute)
				}
				continue
			}
			actual, err := skills.ReadTree(absolute)
			if err != nil {
				plan.Collisions = append(plan.Collisions, NativeCollision{ID: entry.ID, Path: absolute, Kind: kind, Message: "Cannot inspect installed skill: " + err.Error()})
				continue
			}
			observed = append(observed, absolute+":"+treeFingerprint(actual))
			resolved, _ := filepath.EvalSymlinks(absolute)
			resolvedSource, _ := filepath.EvalSymlinks(library)
			if kind == "link" && resolvedSource != "" && resolved == resolvedSource {
				if !a.skillWanted(entry) {
					plan.Pending = append(plan.Pending, absolute)
				}
				continue
			}
			message := "Existing skill needs native migration; normal sync preserves local directories. Back up and review before replacing it."
			if kind == "link" {
				message = "Existing link points outside the native library; review Skillshare sync preview before handing it over."
			}
			plan.Collisions = append(plan.Collisions, NativeCollision{ID: entry.ID, Path: absolute, Kind: kind, MatchesAccepted: len(skills.Diff(accepted, actual)) == 0, Message: message})
		}
	}
	// Exclude only catalog-controlled names. New skills added in the native app
	// remain available and are discovered as untracked by the next audit.
	paths := make([]string, 0, len(byPath))
	for path := range byPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		target := byPath[path]
		desired := map[string]bool{}
		for _, entry := range a.Config.Skills {
			if entry.Owner != "skillshare" {
				continue
			}
			selected := a.skillWanted(entry) && contains(entry.Locations, relativeLocation(a.Home, filepath.Join(path, entry.Name)))
			if !selected {
				desired[entry.Name] = true
			} else {
				if len(target.Include) > 0 && !nativeMatches(target.Include, entry.Name) {
					plan.Issues = append(plan.Issues, "native target "+target.Name+" include filters exclude selected skill "+entry.Name)
				}
				for _, pattern := range target.Exclude {
					if !contains(state.Excludes[target.Name], pattern) && nativeMatches([]string{pattern}, entry.Name) {
						plan.Issues = append(plan.Issues, "native target "+target.Name+" manually excludes selected skill "+entry.Name)
					}
				}
			}
		}
		for name := range desired {
			if !contains(target.Exclude, name) {
				target.AddExclude = append(target.AddExclude, name)
			}
		}
		for _, name := range state.Excludes[target.Name] {
			if !desired[name] && contains(target.Exclude, name) {
				target.RemoveExclude = append(target.RemoveExclude, name)
			}
		}
		sort.Strings(target.AddExclude)
		sort.Strings(target.RemoveExclude)
		plan.Targets = append(plan.Targets, *target)
	}
	sort.Slice(plan.Imports, func(i, j int) bool { return plan.Imports[i].ID < plan.Imports[j].ID })
	sort.Strings(observed)
	plan.Ready = plan.Initialized && len(plan.Issues) == 0 && len(plan.Collisions) == 0 && len(plan.Pending) == 0
	for _, imp := range plan.Imports {
		plan.Ready = plan.Ready && imp.Present
	}
	for _, target := range plan.Targets {
		plan.Ready = plan.Ready && target.Existing && len(target.AddExclude) == 0 && len(target.RemoveExclude) == 0
	}
	digestInput, _ := json.Marshal(struct {
		Plan     NativePlan
		Config   Config
		Native   string
		State    nativeState
		Observed []string
	}{plan, a.Config, string(configBytes), state, observed})
	hash := sha256.Sum256(digestInput)
	plan.Revision = hex.EncodeToString(hash[:])
	return plan, nil
}
func relativeLocation(home, path string) string {
	value, err := filepath.Rel(home, path)
	if err != nil {
		return ""
	}
	return value
}
func nativeTargetName(home, path string) string {
	switch relativeLocation(home, path) {
	case ".agents/skills":
		return "universal"
	case ".codex/skills":
		return "codex"
	case ".claude/skills":
		return "claude"
	}
	hash := sha256.Sum256([]byte(path))
	return "dotagents-" + hex.EncodeToString(hash[:4])
}
func nativeUpstream(source string) (string, string) {
	at := strings.LastIndex(source, "@")
	if at >= 0 && len(source[at+1:]) == 40 {
		if _, err := hex.DecodeString(source[at+1:]); err == nil {
			return source[:at], source[at+1:]
		}
	}
	return source, ""
}
func nativeSource(source string) bool {
	source = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(source, "https://github.com/"), "github.com/"), "git@github.com:")
	if strings.ContainsAny(source, "?&# \\:\t\n") {
		return false
	}
	parts := strings.Split(source, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "." || part == ".." || part == "" || !identifier.MatchString(part) {
			return false
		}
	}
	return true
}

// ConnectSkillshare changes only the native library/configuration. Skillshare
// remains the sole publisher; this method never invokes sync or force.
func (a *App) ConnectSkillshare(ctx context.Context, phase, revision string) (NativeSkillshareResult, error) {
	result := NativeSkillshareResult{Phase: phase, Commands: []string{}}
	if phase != "prepare" && phase != "policy" {
		return result, errors.New("choose phase prepare or policy")
	}
	fresh, err := a.Reload()
	if err != nil {
		return result, err
	}
	plan, err := fresh.NativeSkillsharePlan(ctx)
	if err != nil {
		return result, err
	}
	result.Plan = plan
	if revision != "" && revision != plan.Revision {
		return result, errors.New("native Skillshare plan changed; review a fresh plan")
	}
	if len(plan.Issues) > 0 {
		return result, fmt.Errorf("native Skillshare plan is blocked: %s", strings.Join(plan.Issues, "; "))
	}
	state, err := fresh.readNativeState()
	if err != nil {
		return result, err
	}
	state.ConfigPath = plan.ConfigPath
	state.Source = plan.Source
	if plan.Initialized {
		if _, err = fresh.BackupNativeConfig(plan.ConfigPath); err != nil {
			return result, err
		}
	}
	run := func(args ...string) error {
		_, err := fresh.SkillshareCommand(ctx, args...)
		if err == nil {
			result.Commands = append(result.Commands, strings.Join(append([]string{"skillshare"}, args...), " "))
		}
		return err
	}
	if phase == "prepare" {
		if !plan.Initialized {
			if err = run("init", "--source", plan.Source, "--no-targets", "--no-copy", "--no-git", "--no-skill"); err != nil {
				return result, err
			}
		}
		// Persist the connection before imports: interrupted imports are retryable,
		// and another dotagents command must not become a second native publisher.
		if err = fresh.writeNativeState(state); err != nil {
			return result, err
		}
		groups := map[string][]NativeImport{}
		keys := []string{}
		for _, imp := range plan.Imports {
			if imp.Present {
				continue
			}
			key := imp.Source + "\x00" + imp.Ref
			if _, ok := groups[key]; !ok {
				keys = append(keys, key)
			}
			groups[key] = append(groups[key], imp)
		}
		sort.Strings(keys)
		for _, key := range keys {
			imports := groups[key]
			names := []string{}
			for _, imp := range imports {
				names = append(names, imp.Name)
			}
			args := []string{"install", imports[0].Source, "--skill", strings.Join(names, ","), "--kind", "skill"}
			if len(imports) == 1 {
				args = append(args, "--name", imports[0].Name)
			}
			if imports[0].Ref != "" {
				args = append(args, "--branch", imports[0].Ref)
			}
			if err = run(args...); err != nil {
				return result, err
			}
			for _, name := range names {
				if _, err = skills.ReadTree(filepath.Join(plan.Source, name)); err != nil {
					return result, fmt.Errorf("Skillshare did not install expected native name %s: %w", name, err)
				}
			}
		}
		result.Message = "Native Skillshare library prepared. Accepted snapshots are unchanged. Review differences and migration collisions before native sync."
	} else {
		if !plan.Initialized {
			return result, errors.New("prepare the native Skillshare library first")
		}
		for _, imp := range plan.Imports {
			if !imp.Present {
				return result, fmt.Errorf("native skill %s is missing; run prepare first", imp.Name)
			}
		}
		for _, collision := range plan.Collisions {
			if !collision.MatchesAccepted {
				return result, fmt.Errorf("refusing handoff over an edited or unreadable skill: %s", collision.Path)
			}
		}
		for _, target := range plan.Targets {
			if !target.Existing {
				if err = os.MkdirAll(target.Path, 0700); err != nil {
					return result, err
				}
				if err = run("target", "add", target.Name, target.Path, "--no-skills"); err != nil {
					return result, err
				}
			}
			for _, name := range target.RemoveExclude {
				if err = run("target", target.Name, "--remove-exclude", name); err != nil {
					return result, err
				}
				state.Excludes[target.Name] = withoutNativeName(state.Excludes[target.Name], name)
				if err = fresh.writeNativeState(state); err != nil {
					return result, err
				}
			}
			for _, name := range target.AddExclude {
				if err = run("target", target.Name, "--add-exclude", name); err != nil {
					return result, err
				}
				if !contains(state.Excludes[target.Name], name) {
					state.Excludes[target.Name] = append(state.Excludes[target.Name], name)
					sort.Strings(state.Excludes[target.Name])
				}
				if err = fresh.writeNativeState(state); err != nil {
					return result, err
				}
			}
			if !target.Existing {
				if err = run("target", target.Name, "--skills=true"); err != nil {
					return result, err
				}
			}
		}
		if err = fresh.writeNativeState(state); err != nil {
			return result, err
		}
		result.Message = "Machine policy saved in native Skillshare. Run skillshare sync --global --dry-run, then sync in Skillshare when its preview is right. Existing migration collisions still require review."
	}
	result.Plan, err = fresh.NativeSkillsharePlan(ctx)
	return result, err
}
func withoutNativeName(values []string, name string) []string {
	out := []string{}
	for _, value := range values {
		if value != name {
			out = append(out, value)
		}
	}
	return out
}

type nativeOutput struct {
	bytes.Buffer
	truncated bool
}

func (b *nativeOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := (1 << 20) - b.Len()
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	_, err := b.Buffer.Write(data)
	return n, err
}

// BackupNativeConfig keeps a verified private copy before native CLI config
// mutations. Skillshare remains responsible for its own target backups.
func (a *App) BackupNativeConfig(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("refusing non-regular native config: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if len(data) > 4<<20 {
		return "", errors.New("native config exceeds 4 MiB")
	}
	hash := sha256.Sum256(data)
	directory := filepath.Join(a.Repo, ".dotagents", "backups", "native-config", time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err = os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	backup := filepath.Join(directory, filepath.Base(path))
	if err = atomicWrite(backup, data, 0600); err != nil {
		return "", err
	}
	verified, err := os.ReadFile(backup)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(data, verified) {
		return "", errors.New("native config backup verification failed")
	}
	receipt, _ := json.MarshalIndent(struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Mode   uint32 `json:"mode"`
	}{path, hex.EncodeToString(hash[:]), uint32(info.Mode().Perm())}, "", "  ")
	if err = atomicWrite(filepath.Join(directory, "receipt.json"), append(receipt, '\n'), 0600); err != nil {
		return "", err
	}
	return backup, nil
}

func nativeWithin(root, path string) bool {
	if pathWithin(root, path) {
		return true
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		canonicalRoot = root
	}
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		canonicalPath = path
	}
	return pathWithin(canonicalRoot, canonicalPath)
}
func nativeMatches(patterns []string, name string) bool {
	for _, pattern := range patterns {
		matched, err := filepath.Match(pattern, name)
		if err == nil && matched {
			return true
		}
	}
	return false
}

type nativeProvenance struct {
	Source  string `json:"source"`
	RepoURL string `json:"repo_url"`
	Type    string `json:"type"`
}

func nativeMetadata(source string) (map[string]nativeProvenance, error) {
	out := map[string]nativeProvenance{}
	path := filepath.Join(source, ".metadata.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return out, errors.New("native metadata must be a regular file no larger than 8 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return out, err
	}
	if len(data) > 8<<20 {
		return out, errors.New("native metadata exceeds 8 MiB")
	}
	var document struct {
		Version int                         `json:"version"`
		Entries map[string]nativeProvenance `json:"entries"`
	}
	if err = json.Unmarshal(data, &document); err != nil {
		return out, err
	}
	if document.Version != 1 {
		return out, errors.New("unsupported native metadata version")
	}
	return document.Entries, nil
}
func nativeRepository(source string) string {
	source = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(source, "https://github.com/"), "github.com/"), "git@github.com:")
	parts := strings.Split(source, "/")
	if len(parts) < 2 {
		return ""
	}
	return strings.ToLower(parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"))
}
