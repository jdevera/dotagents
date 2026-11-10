package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/jdevera/dotagents/internal/integrations"
	"github.com/jdevera/dotagents/internal/mcp"
	"github.com/jdevera/dotagents/internal/skills"
	"github.com/pelletier/go-toml/v2"
)

type MCPPlan struct {
	Revision string      `json:"revision"`
	Actions  []MCPAction `json:"actions"`
	Issues   []string    `json:"issues"`
}

type MCPAction struct {
	Client  string          `json:"client"`
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Path    string          `json:"path"`
	Changes []skills.Change `json:"changes"`
}

type mcpFileState struct {
	Exists bool   `json:"exists"`
	Mode   uint32 `json:"mode"`
	Hash   string `json:"hash"`
	Data   []byte `json:"-"`
}

type preparedMCP struct {
	Plan     MCPPlan
	Stage    integrations.MCPStage
	Observed map[string]mcpFileState
}

// MCPPlan reads live settings and prepares prospective changes in temporary
// account directories. Returned actions contain redacted diffs only.
func (a *App) MCPPlan(ctx context.Context) (MCPPlan, error) {
	prepared, err := a.prepareMCP(ctx, a.mcpStager())
	return prepared.Plan, err
}

type mcpStager func(context.Context, string, []integrations.MCPDesired) (integrations.MCPStage, error)

func (a *App) mcpStager() mcpStager {
	if a.Config.Scope == "project" {
		return integrations.StageMCPProject
	}
	return integrations.StageMCP
}

func (a *App) prepareMCP(ctx context.Context, stage mcpStager) (preparedMCP, error) {
	prepared := preparedMCP{Plan: MCPPlan{Actions: []MCPAction{}, Issues: []string{}}, Observed: map[string]mcpFileState{}}
	if err := a.Config.Validate(); err != nil {
		return prepared, err
	}
	byClient := map[string]map[string]any{}
	failed := map[string]bool{}
	for _, rule := range a.Config.MCP {
		if _, ok := byClient[rule.Client]; ok || failed[rule.Client] {
			continue
		}
		relative, field, err := mcpNativePath(rule.Client, a.Config.Scope)
		if err != nil {
			prepared.Plan.Issues = append(prepared.Plan.Issues, err.Error())
			failed[rule.Client] = true
			continue
		}
		state, err := inspectMCPFile(a.Home, relative, a.Config.Scope)
		if err != nil {
			prepared.Plan.Issues = append(prepared.Plan.Issues, err.Error())
			failed[rule.Client] = true
			continue
		}
		prepared.Observed[relative] = state
		document, err := decodeMCPFile(rule.Client, state.Data)
		if err != nil {
			prepared.Plan.Issues = append(prepared.Plan.Issues, err.Error())
			failed[rule.Client] = true
			continue
		}
		servers := map[string]any{}
		if raw, exists := document[field]; exists {
			var ok bool
			servers, ok = raw.(map[string]any)
			if !ok {
				prepared.Plan.Issues = append(prepared.Plan.Issues, rule.Client+": native MCP table is invalid")
				failed[rule.Client] = true
				continue
			}
		}
		byClient[rule.Client] = servers
	}
	desired := []integrations.MCPDesired{}
	for _, rule := range a.Config.MCP {
		if failed[rule.Client] {
			continue
		}
		relative, _, _ := mcpNativePath(rule.Client, a.Config.Scope)
		raw, exists := byClient[rule.Client][rule.Name]
		var installed map[string]any
		if exists {
			var ok bool
			installed, ok = raw.(map[string]any)
			if !ok {
				prepared.Plan.Issues = append(prepared.Plan.Issues, rule.Client+"/"+rule.Name+": installed definition is invalid")
				continue
			}
		}
		wanted := a.wanted(rule.Groups, nil)
		if !wanted {
			if exists {
				desired = append(desired, integrations.MCPDesired{Client: rule.Client, Name: rule.Name, Remove: true})
				prepared.Plan.Actions = append(prepared.Plan.Actions, MCPAction{Client: rule.Client, Name: rule.Name, Kind: "remove", Path: filepath.Join(a.Home, relative), Changes: mcp.DefinitionDiff(installed, nil)})
			}
			continue
		}
		accepted, err := a.acceptedMCP(rule)
		if err != nil {
			prepared.Plan.Issues = append(prepared.Plan.Issues, rule.Client+"/"+rule.Name+": full accepted definition is unavailable or damaged; restore the private snapshot, not the redacted catalog view")
			continue
		}
		if exists && mcp.Equivalent(rule.Client, installed, accepted) {
			continue
		}
		kind := "update"
		if !exists {
			kind = "install"
		}
		desired = append(desired, integrations.MCPDesired{Client: rule.Client, Name: rule.Name, Definition: accepted})
		prepared.Plan.Actions = append(prepared.Plan.Actions, MCPAction{Client: rule.Client, Name: rule.Name, Kind: kind, Path: filepath.Join(a.Home, relative), Changes: mcp.DefinitionDiffFor(rule.Client, installed, accepted)})
	}
	if len(prepared.Plan.Issues) == 0 && len(desired) > 0 {
		staged, err := stage(ctx, a.Home, desired)
		if err != nil {
			prepared.Plan.Issues = append(prepared.Plan.Issues, err.Error())
		} else {
			for relative, before := range staged.Before {
				observed, ok := prepared.Observed[relative]
				if !ok || !bytes.Equal(observed.Data, before) || (observed.Exists && before == nil) {
					prepared.Plan.Issues = append(prepared.Plan.Issues, "native settings changed while preparing the plan; rerun mcp-plan")
				}
			}
			for relative := range staged.Files {
				if _, ok := prepared.Observed[relative]; !ok || !validMCPRelative(relative, a.Config.Scope) {
					prepared.Plan.Issues = append(prepared.Plan.Issues, "publisher returned an unexpected native path")
				}
			}
			prepared.Stage = staged
		}
	}
	sort.Slice(prepared.Plan.Actions, func(i, j int) bool {
		left, right := prepared.Plan.Actions[i], prepared.Plan.Actions[j]
		if left.Client != right.Client {
			return left.Client < right.Client
		}
		return left.Name < right.Name
	})
	sort.Strings(prepared.Plan.Issues)
	outputs := map[string]string{}
	for path, data := range prepared.Stage.Files {
		outputs[path] = hashBytes(data)
	}
	revision, err := json.Marshal(struct {
		Config   Config
		Machine  Machine
		Observed map[string]mcpFileState
		Outputs  map[string]string
		Actions  []MCPAction
		Issues   []string
	}{a.Config, a.Machine, prepared.Observed, outputs, prepared.Plan.Actions, prepared.Plan.Issues})
	if err != nil {
		return prepared, err
	}
	prepared.Plan.Revision = hashBytes(revision)
	return prepared, nil
}

type mcpTransaction struct {
	Version  int                   `json:"version"`
	ID       string                `json:"id"`
	Status   string                `json:"status"`
	Revision string                `json:"revision"`
	Created  string                `json:"created_at"`
	Home     string                `json:"home"`
	Repo     string                `json:"repo"`
	Scope    string                `json:"scope,omitempty"`
	Entries  []mcpTransactionEntry `json:"entries"`
}

type mcpTransactionEntry struct {
	Relative   string `json:"relative"`
	Existed    bool   `json:"existed"`
	BeforeHash string `json:"before_hash"`
	BeforeMode uint32 `json:"before_mode"`
	AfterHash  string `json:"after_hash"`
	AfterMode  uint32 `json:"after_mode"`
}

func (a *App) MCPApply(ctx context.Context, revision string) (string, error) {
	return a.mcpApply(ctx, revision, a.mcpStager())
}

func (a *App) mcpApply(ctx context.Context, revision string, stage mcpStager) (string, error) {
	if revision == "" {
		return "", errors.New("mcp-apply requires the revision from mcp-plan")
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
	prepared, err := fresh.prepareMCP(ctx, stage)
	if err != nil {
		return "", err
	}
	if prepared.Plan.Revision != revision {
		return "", errors.New("MCP plan is stale; run mcp-plan again")
	}
	if len(prepared.Plan.Issues) > 0 {
		return "", errors.New("MCP plan has unresolved issues; no live settings changed")
	}
	if len(prepared.Stage.Files) == 0 {
		return "", nil
	}
	id, err := newTransactionID()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(a.Repo, ".dotagents", "mcp-transactions", id)
	if err := ensureRealDirectory(filepath.Dir(dir)); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	record := mcpTransaction{Version: 1, ID: id, Status: "prepared", Revision: revision, Created: time.Now().UTC().Format(time.RFC3339Nano), Home: a.Home, Repo: a.Repo, Scope: a.Config.Scope, Entries: []mcpTransactionEntry{}}
	names := make([]string, 0, len(prepared.Stage.Files))
	for name := range prepared.Stage.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		before := prepared.Observed[name]
		after := prepared.Stage.Files[name]
		mode := uint32(0600)
		if before.Exists {
			mode = before.Mode
		}
		entry := mcpTransactionEntry{Relative: name, Existed: before.Exists, BeforeHash: before.Hash, BeforeMode: before.Mode, AfterHash: hashBytes(after), AfterMode: mode}
		if before.Exists {
			if err := privateMCPFile(dir, fmt.Sprintf("before-%04d", i), before.Data); err != nil {
				return id, err
			}
		}
		if err := privateMCPFile(dir, fmt.Sprintf("after-%04d", i), after); err != nil {
			return id, err
		}
		record.Entries = append(record.Entries, entry)
	}
	if err := writeMCPTransaction(dir, record); err != nil {
		return id, err
	}
	// Recheck the catalog and every observed native file after all backups exist.
	latest, err := a.Reload()
	if err != nil || latest.configDigest != fresh.configDigest {
		return id, errors.New("catalog changed during preparation; no live settings changed")
	}
	for path, expected := range prepared.Observed {
		actual, err := inspectMCPFile(a.Home, path, a.Config.Scope)
		if err != nil || !sameMCPState(actual, expected) {
			return id, errors.New("native settings changed during preparation; no live settings changed")
		}
	}
	record.Status = "applying"
	if err := writeMCPTransaction(dir, record); err != nil {
		return id, err
	}
	for i, entry := range record.Entries {
		if err = ctx.Err(); err == nil {
			err = a.publishMCPFile(dir, id, i, entry)
		}
		if err != nil {
			restoreErr := a.rollbackMCP(dir, &record)
			if restoreErr != nil {
				return id, fmt.Errorf("MCP apply failed (%v); automatic recovery needs attention (%v); private backups retained at %s", err, restoreErr, dir)
			}
			return id, fmt.Errorf("MCP apply failed; original settings restored: %w", err)
		}
	}
	record.Status = "applied"
	if err := writeMCPTransaction(dir, record); err != nil {
		return id, fmt.Errorf("settings applied but completion could not be recorded; backups at %s", dir)
	}
	if err := a.cleanupMCPTemps(id, record.Entries); err != nil {
		return id, err
	}
	return id, nil
}

func privateMCPFile(dir, name string, data []byte) error {
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, data) {
		return errors.New("private MCP backup verification failed")
	}
	return nil
}

func writeMCPTransaction(dir string, record mcpTransaction) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "transaction.json"), append(data, '\n'), 0600)
}

func (a *App) publishMCPFile(dir, id string, i int, entry mcpTransactionEntry) error {
	actual, err := inspectMCPFile(a.Home, entry.Relative, a.Config.Scope)
	if err != nil || !sameMCPState(actual, beforeMCPState(entry)) {
		return fmt.Errorf("native settings changed before publication: %s", entry.Relative)
	}
	staged, err := readVerifiedMCPFile(dir, fmt.Sprintf("after-%04d", i), entry.AfterHash)
	if err != nil {
		return err
	}
	path := filepath.Join(a.Home, entry.Relative)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := safeMCPDestination(a.Home, entry.Relative, a.Config.Scope); err != nil {
		return err
	}
	old, next := mcpTempPaths(a.Home, id, i, entry.Relative)
	if err := privateMCPFile(filepath.Dir(next), filepath.Base(next), staged); err != nil {
		return err
	}
	if err := os.Chmod(next, os.FileMode(entry.AfterMode)); err != nil {
		return err
	}
	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		return errors.New("MCP recovery path already exists")
	}
	actual, err = inspectMCPFile(a.Home, entry.Relative, a.Config.Scope)
	if err != nil || !sameMCPState(actual, beforeMCPState(entry)) {
		return errors.New("native settings changed during publication preparation")
	}
	if entry.Existed {
		if err := os.Rename(path, old); err != nil {
			return err
		}
		moved, err := inspectMCPRegular(old)
		if err != nil || !sameMCPState(moved, beforeMCPState(entry)) {
			return errors.New("native settings changed during capture; original retained beside target")
		}
	}
	// Link publishes atomically only if the destination is still absent. Rename
	// would overwrite a file created by an editor during the capture above.
	if err := os.Link(next, path); err != nil {
		return err
	}
	if err := os.Remove(next); err != nil {
		return err
	}
	published, err := inspectMCPFile(a.Home, entry.Relative, a.Config.Scope)
	if err != nil || !sameMCPState(published, afterMCPState(entry)) {
		return errors.New("published MCP settings did not verify")
	}
	return nil
}

func (a *App) MCPRollback(id string) error {
	if !identifier.MatchString(id) {
		return errors.New("invalid MCP transaction ID")
	}
	unlock, err := a.operationLock()
	if err != nil {
		return err
	}
	defer unlock()
	dir := filepath.Join(a.Repo, ".dotagents", "mcp-transactions", id)
	for _, path := range []string{filepath.Dir(dir), dir} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid MCP transaction directory")
		}
	}
	manifest := filepath.Join(dir, "transaction.json")
	info, err := os.Lstat(manifest)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return errors.New("invalid MCP transaction manifest")
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	var record mcpTransaction
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return errors.New("invalid MCP transaction manifest")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("MCP transaction contains trailing data")
	}
	if record.Version != 1 || record.ID != id || record.Home != a.Home || record.Repo != a.Repo || record.Scope != a.Config.Scope {
		return errors.New("MCP transaction belongs to another repository or home")
	}
	if record.Status == "rolled_back" {
		return nil
	}
	switch record.Status {
	case "prepared", "applying", "applied", "rolling_back":
	default:
		return errors.New("MCP transaction has an unknown status")
	}
	return a.rollbackMCP(dir, &record)
}

func (a *App) rollbackMCP(dir string, record *mcpTransaction) error {
	seen := map[string]bool{}
	for i, entry := range record.Entries {
		if !validMCPRelative(entry.Relative, record.Scope) || seen[entry.Relative] || entry.BeforeMode > 0777 || entry.AfterMode > 0777 {
			return errors.New("invalid MCP transaction entry")
		}
		seen[entry.Relative] = true
		if entry.Existed {
			if _, err := readVerifiedMCPFile(dir, fmt.Sprintf("before-%04d", i), entry.BeforeHash); err != nil {
				return err
			}
		}
		if _, err := readVerifiedMCPFile(dir, fmt.Sprintf("after-%04d", i), entry.AfterHash); err != nil {
			return err
		}
		current, err := inspectMCPFile(a.Home, entry.Relative, a.Config.Scope)
		if err != nil {
			return err
		}
		if sameMCPState(current, beforeMCPState(entry)) || sameMCPState(current, afterMCPState(entry)) {
			continue
		}
		// An interrupted capture may leave no live file and the original next to
		// it. Applied transactions never treat a later deletion as our own work.
		if !current.Exists && record.Status != "applied" {
			old, _ := mcpTempPaths(a.Home, record.ID, i, entry.Relative)
			captured, err := inspectMCPRegular(old)
			if err == nil && sameMCPState(captured, beforeMCPState(entry)) {
				continue
			}
			if record.Status == "rolling_back" {
				retired, err := inspectMCPRegular(old + "-applied")
				if err == nil && sameMCPState(retired, afterMCPState(entry)) {
					continue
				}
			}
		}
		return fmt.Errorf("refusing MCP rollback: %s changed since apply", entry.Relative)
	}
	record.Status = "rolling_back"
	if err := writeMCPTransaction(dir, *record); err != nil {
		return err
	}
	for i := len(record.Entries) - 1; i >= 0; i-- {
		entry := record.Entries[i]
		current, err := inspectMCPFile(a.Home, entry.Relative, a.Config.Scope)
		if err != nil {
			return err
		}
		if sameMCPState(current, beforeMCPState(entry)) {
			continue
		}
		if current.Exists && !sameMCPState(current, afterMCPState(entry)) {
			return fmt.Errorf("native settings changed during MCP rollback: %s", entry.Relative)
		}
		path := filepath.Join(a.Home, entry.Relative)
		old, next := mcpTempPaths(a.Home, record.ID, i, entry.Relative)
		if entry.Existed {
			original, err := readVerifiedMCPFile(dir, fmt.Sprintf("before-%04d", i), entry.BeforeHash)
			if err != nil {
				return err
			}
			if leftover, err := inspectMCPRegular(next); err == nil {
				if !sameMCPState(leftover, beforeMCPState(entry)) && !sameMCPState(leftover, afterMCPState(entry)) {
					return errors.New("unexpected data in MCP recovery staging path")
				}
				if err := os.Remove(next); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := privateMCPFile(filepath.Dir(next), filepath.Base(next), original); err != nil {
				return err
			}
			if err := os.Chmod(next, os.FileMode(entry.BeforeMode)); err != nil {
				return err
			}
		}
		retired := old + "-applied"
		if current.Exists {
			if _, err := os.Lstat(retired); !os.IsNotExist(err) {
				return errors.New("MCP recovery retirement path already exists")
			}
			if err := os.Rename(path, retired); err != nil {
				return err
			}
			captured, err := inspectMCPRegular(retired)
			if err != nil || !sameMCPState(captured, afterMCPState(entry)) {
				return errors.New("native settings changed during rollback capture; retained beside target")
			}
		}
		if entry.Existed {
			if err := os.Link(next, path); err != nil {
				return err
			}
			if err := os.Remove(next); err != nil {
				return err
			}
		}
		if current.Exists {
			if err := removeMCPTemp(retired, afterMCPState(entry)); err != nil {
				return err
			}
		}
		verified, err := inspectMCPFile(a.Home, entry.Relative, a.Config.Scope)
		if err != nil || !sameMCPState(verified, beforeMCPState(entry)) {
			return errors.New("restored MCP settings failed verification")
		}
	}
	if err := a.cleanupMCPTemps(record.ID, record.Entries); err != nil {
		return err
	}
	record.Status = "rolled_back"
	return writeMCPTransaction(dir, *record)
}

func (a *App) cleanupMCPTemps(id string, entries []mcpTransactionEntry) error {
	for i, entry := range entries {
		old, next := mcpTempPaths(a.Home, id, i, entry.Relative)
		if entry.Existed {
			if err := removeMCPTemp(old, beforeMCPState(entry)); err != nil {
				return err
			}
		}
		if state, err := inspectMCPRegular(next); err == nil {
			if !sameMCPState(state, beforeMCPState(entry)) && !sameMCPState(state, afterMCPState(entry)) {
				return errors.New("unexpected MCP staging contents retained")
			}
			if err := os.Remove(next); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := removeMCPTemp(old+"-applied", afterMCPState(entry)); err != nil {
			return err
		}
	}
	return nil
}

func removeMCPTemp(path string, expected mcpFileState) error {
	actual, err := inspectMCPRegular(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !sameMCPState(actual, expected) {
		return errors.New("MCP temporary contents changed; retained for recovery")
	}
	return os.Remove(path)
}

func readVerifiedMCPFile(dir, name, hash string) ([]byte, error) {
	state, err := inspectMCPRegular(filepath.Join(dir, name))
	if err != nil || state.Hash != hash {
		return nil, errors.New("MCP backup or staged contents failed verification")
	}
	return state.Data, nil
}

func beforeMCPState(entry mcpTransactionEntry) mcpFileState {
	return mcpFileState{Exists: entry.Existed, Mode: entry.BeforeMode, Hash: entry.BeforeHash}
}

func afterMCPState(entry mcpTransactionEntry) mcpFileState {
	return mcpFileState{Exists: true, Mode: entry.AfterMode, Hash: entry.AfterHash}
}

func sameMCPState(a, b mcpFileState) bool {
	return a.Exists == b.Exists && a.Mode == b.Mode && a.Hash == b.Hash
}

func mcpTempPaths(home, id string, i int, relative string) (old, next string) {
	parent := filepath.Dir(filepath.Join(home, relative))
	suffix := id + fmt.Sprintf("-%04d", i)
	return filepath.Join(parent, ".dotagents-mcp-old-"+suffix), filepath.Join(parent, ".dotagents-mcp-new-"+suffix)
}

func validMCPRelative(relative, scope string) bool {
	if relative == ".codex/config.toml" {
		return true
	}
	if scope == "project" {
		return relative == ".mcp.json"
	}
	return (scope == "" || scope == "home") && relative == ".claude.json"
}

func mcpNativePath(client, scope string) (relative, field string, err error) {
	switch client {
	case "claude":
		if scope == "project" {
			return ".mcp.json", "mcpServers", nil
		}
		return ".claude.json", "mcpServers", nil
	case "codex":
		return ".codex/config.toml", "mcp_servers", nil
	default:
		return "", "", fmt.Errorf("unsupported MCP client %q", client)
	}
}

func safeMCPDestination(home, relative, scope string) error {
	if !validMCPRelative(relative, scope) {
		return errors.New("unexpected MCP destination")
	}
	parent := filepath.Dir(relative)
	if parent != "." {
		info, err := os.Lstat(filepath.Join(home, parent))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("MCP configuration parent is a link or non-directory; automatic publication refused")
		}
	}
	info, err := os.Lstat(filepath.Join(home, relative))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("MCP configuration is a link or non-regular file; automatic publication refused")
	}
	return nil
}

func inspectMCPFile(home, relative, scope string) (mcpFileState, error) {
	if err := safeMCPDestination(home, relative, scope); err != nil {
		return mcpFileState{}, err
	}
	state, err := inspectMCPRegular(filepath.Join(home, relative))
	if os.IsNotExist(err) {
		return mcpFileState{}, nil
	}
	return state, err
}

func inspectMCPRegular(path string) (mcpFileState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return mcpFileState{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return mcpFileState{}, errors.New("MCP settings must be a regular file below 16 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return mcpFileState{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return mcpFileState{}, errors.New("MCP file changed during inspection")
	}
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		return mcpFileState{}, errors.New("cannot read bounded MCP settings")
	}
	return mcpFileState{Exists: true, Mode: uint32(info.Mode().Perm()), Hash: hashBytes(data), Data: data}, nil
}

func decodeMCPFile(client string, data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var result map[string]any
	var err error
	if client == "codex" {
		err = toml.Unmarshal(data, &result)
	} else {
		err = json.Unmarshal(data, &result)
	}
	if err != nil || result == nil {
		return nil, errors.New("invalid native MCP settings; contents withheld")
	}
	return result, nil
}
