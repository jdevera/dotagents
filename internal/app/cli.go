package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jdevera/dotagents/internal/dashboard"
	"github.com/jdevera/dotagents/internal/providers"
)

var Version = "dev"

const usage = `dotagents — know what your agents are running

Read-only commands:
  scan                 Discover home-level skills and MCP definitions
  check                Compare installed, accepted and current tool contents
  diff ID              Show file changes for one tracked skill
  probe td|jev         Inspect the current tool's output without running it
  refresh ID          Compare a repository skill against its latest upstream
  plan                 Preview changes needed to install accepted skills
  mcp-plan             Preview selected MCP settings through Skillshare
  serve                Open a local, read-only dashboard (manual refresh)
  export --output FILE Save a standalone dashboard you can open offline

Explicit changes:
  init                 Create an empty catalog in --repo
  backup               Archive and verify the repository and affected home files
  adopt PATH           Track an installed skill and save its actual files
  add-tool td|jev       Accept current tool output and select its agent targets
  fetch SOURCE --skill NAME --ref COMMIT --targets codex,claude
                       Fetch a pinned skill through Skillshare; accept, don't install
  tag ID --topics TOPIC [--scope home|project]
                       Group accepted skills for one-command project installation
  project TOPIC --directory PATH [--targets codex,claude]
                       Install a topic in a project, with backups and rollback
  accept ID --from installed|tool
                       Accept a reviewed new snapshot; does not install it
  ignore PATH --reason TEXT
                       Record why an untracked skill should be left alone
  track-mcp NAME --client claude|codex
                       Save a private definition and a redacted catalog entry
  define-mcp NAME --client CLIENT --file JSON
                       Version an explicitly authored portable MCP definition
  apply --revision HASH
                       Apply a reviewed plan, with verified backup first
  rollback ID          Restore a transaction if nothing changed afterwards
  mcp-apply --revision HASH
                       Publish a reviewed MCP plan with a private backup
  mcp-rollback ID      Restore an MCP transaction without losing later edits

Common options:
  --repo DIR           Catalog repository (default current directory)
  --home DIR           Home to inspect/manage (default your home)
  --json               Machine-readable output
  --groups mac,work    Select machine groups when adopting
  --owner OWNER        manual, skillshare, td, jev, native (default manual)
  --source URL         Record the source when adopting
  --id ID              Choose an ID when adopting
  --path PATH          Explicit tool installation for probe
  --addr ADDRESS       Loopback dashboard address (default 127.0.0.1:7391)

Checks never install, update, accept, schedule or start an MCP server.
Exit codes: 0 success/clean, 1 findings, 2 command/configuration failure.
`

func Run(ctx context.Context, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(out, usage)
		return 0
	}
	command := args[0]
	if command == "help" || command == "--help" || command == "-h" {
		fmt.Fprint(out, usage)
		return 0
	}
	if command == "version" || command == "--version" {
		fmt.Fprintln(out, "dotagents", Version)
		return 0
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	defaultRepo := os.Getenv("DOTAGENTS_REPO")
	if defaultRepo == "" {
		defaultRepo = "."
	}
	repo := fs.String("repo", defaultRepo, "catalog repository")
	home := fs.String("home", "", "home to inspect")
	jsonOutput := fs.Bool("json", false, "JSON output")
	owner := fs.String("owner", "manual", "content owner")
	source := fs.String("source", "", "source URL")
	id := fs.String("id", "", "catalog ID")
	groups := fs.String("groups", "", "comma separated groups")
	from := fs.String("from", "", "installed or tool")
	toolPath := fs.String("path", "", "explicit tool installation")
	addr := fs.String("addr", "127.0.0.1:7391", "loopback listen address")
	output := fs.String("output", "", "output file")
	revision := fs.String("revision", "", "reviewed plan revision")
	reason := fs.String("reason", "", "reason to ignore")
	client := fs.String("client", "", "MCP client: claude or codex")
	skillName := fs.String("skill", "", "upstream skill name")
	ref := fs.String("ref", "", "full pinned source commit")
	targets := fs.String("targets", "", "receiving agents: codex,claude")
	topics := fs.String("topics", "", "comma separated topics")
	scope := fs.String("scope", "", "home or project")
	directory := fs.String("directory", "", "project directory")
	only := fs.String("only", "", "only these skill IDs when planning/applying")
	accept := fs.Bool("accept", false, "accept refreshed upstream contents without installing")
	definitionFile := fs.String("file", "", "portable MCP definition JSON")
	ordered, err := interspersed(fs, args[1:])
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	if err = fs.Parse(ordered); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	pos := fs.Args()
	fail := func(err error) int { fmt.Fprintln(errOut, "dotagents:", err); return 2 }
	if command == "init" {
		if len(pos) != 0 {
			return fail(errors.New("init takes no positional arguments"))
		}
		path := filepath.Join(*repo, "dotagents.json")
		if _, err := os.Lstat(path); err == nil {
			return fail(errors.New("catalog already exists"))
		} else if !os.IsNotExist(err) {
			return fail(err)
		}
		cfg := Config{Version: 1, Groups: map[string]Condition{"mac": {OS: []string{"darwin"}}, "linux": {OS: []string{"linux"}}}, Skills: []Skill{}, Ignore: []Ignore{}, MCP: []MCPRule{}}
		a := &App{Repo: *repo, Config: cfg}
		if err := a.Save(); err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, map[string]string{"catalog": path}, fail)
		}
		fmt.Fprintln(out, "Created", path)
		return 0
	}
	a, err := Open(*repo, *home)
	if err != nil {
		return fail(err)
	}
	a.Only = splitGroups(*only)
	switch command {
	case "backup":
		if len(pos) != 0 {
			return fail(errors.New("backup takes no positional arguments"))
		}
		receipt, err := a.Backup()
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, receipt, fail)
		}
		fmt.Fprintf(out, "Verified backup: %s\n%d files · %d bytes\nSHA-256 %s\nKeep this private: it may contain credentials.\n", receipt.Archive, receipt.Files, receipt.Bytes, receipt.SHA256)
		return 0
	case "scan", "check", "diff", "export":
		if command == "diff" && len(pos) != 1 {
			return fail(errors.New("usage: dotagents diff ID"))
		}
		if command != "diff" && len(pos) != 0 {
			return fail(fmt.Errorf("%s takes no positional arguments", command))
		}
		r := a.Report(ctx)
		if command == "export" {
			if *output == "" {
				return fail(errors.New("export requires --output FILE"))
			}
			b, err := dashboard.Export(r)
			if err != nil {
				return fail(err)
			}
			if err = atomicWrite(*output, b, 0600); err != nil {
				return fail(err)
			}
			fmt.Fprintln(out, "Saved", *output)
			return 0
		}
		if command == "diff" {
			for _, f := range r.Findings {
				if f.ID == pos[0] {
					if *jsonOutput {
						return writeJSON(out, f, fail)
					}
					fmt.Fprintf(out, "%s · %s\n%s\n", f.Name, f.Status, f.Message)
					if f.Status == "unknown" || f.Status == "ignored" {
						fmt.Fprintln(out, "No accepted snapshot exists for this item; content comparison is unavailable.")
						return 0
					}
					fmt.Fprintln(out, "\nAccepted → installed")
					if len(f.InstalledChanges) == 0 {
						fmt.Fprintln(out, "  No content changes detected.")
					}
					for _, c := range f.InstalledChanges {
						fmt.Fprintf(out, "\n%s %s\n%s\n", c.Kind, c.Path, c.Diff)
					}
					if f.Owner == "td" || f.Owner == "jev" {
						fmt.Fprintln(out, "\nAccepted → current tool")
						if f.ProviderError != "" {
							fmt.Fprintln(out, "  Could not compare:", f.ProviderError)
						} else if len(f.GeneratedChanges) == 0 {
							fmt.Fprintln(out, "  No content changes detected.")
						}
						for _, c := range f.GeneratedChanges {
							fmt.Fprintf(out, "\n%s %s\n%s\n", c.Kind, c.Path, c.Diff)
						}
					}
					return 0
				}
			}
			return fail(fmt.Errorf("no finding for %s", pos[0]))
		}
		if *jsonOutput {
			if code := writeJSON(out, r, fail); code != 0 {
				return code
			}
		} else {
			printReport(out, r, command == "scan")
		}
		if command == "check" && r.NeedsAttention() {
			return 1
		}
		return 0
	case "probe":
		if len(pos) != 1 {
			return fail(errors.New("usage: dotagents probe td|jev"))
		}
		result, err := providers.Probe(ctx, pos[0], providers.Options{Home: a.Home, Path: *toolPath})
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, result, fail)
		}
		fmt.Fprintf(out, "%s %s\n%s\n", result.Tool, result.Version, result.Method)
		paths := make([]string, 0, len(result.Files))
		for path := range result.Files {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			fmt.Fprintf(out, "  %s (%d bytes)\n", path, len(result.Files[path].Data))
		}
		return 0
	case "adopt":
		if len(pos) != 1 {
			return fail(errors.New("usage: dotagents adopt PATH [--owner td|jev|skillshare|manual|native]"))
		}
		entry, err := a.Adopt(pos[0], *id, *owner, *source, splitGroups(*groups))
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, entry, fail)
		}
		fmt.Fprintf(out, "Tracked %s. Saved actual files as snapshot %s. Installation unchanged.\n", entry.ID, entry.Snapshot[:12])
		return 0
	case "add-tool":
		if len(pos) != 1 {
			return fail(errors.New("usage: dotagents add-tool td|jev [--targets codex,claude] [--groups GROUPS]"))
		}
		entry, err := a.AddTool(ctx, pos[0], *toolPath, splitGroups(*targets), splitGroups(*groups))
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, entry, fail)
		}
		fmt.Fprintln(out, "Accepted", entry.ID, "from", entry.Source, "— run plan to preview installation.")
		return 0
	case "fetch":
		if len(pos) != 1 || *skillName == "" || *ref == "" {
			return fail(errors.New("usage: dotagents fetch SOURCE --skill NAME --ref FULL_COMMIT --targets codex,claude"))
		}
		entry, err := a.Fetch(ctx, pos[0], *skillName, *ref, splitGroups(*targets), splitGroups(*groups))
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, entry, fail)
		}
		fmt.Fprintln(out, "Fetched and accepted", entry.ID, "from", entry.Source, "— run plan to preview installation.")
		return 0
	case "refresh":
		if len(pos) != 1 {
			return fail(errors.New("usage: dotagents refresh ID [--ref COMMIT] [--accept]"))
		}
		result, err := a.Refresh(ctx, pos[0], *ref, *accept)
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, result, fail)
		}
		fmt.Fprintf(out, "%s at %s\n", result.Source, result.Commit)
		if len(result.Changes) == 0 {
			fmt.Fprintln(out, "Contents match the accepted snapshot.")
		}
		for _, change := range result.Changes {
			fmt.Fprintf(out, "\n%s %s\n%s\n", change.Kind, change.Path, change.Diff)
		}
		if result.Accepted {
			fmt.Fprintln(out, "Accepted. Run plan to preview installation.")
		} else {
			fmt.Fprintln(out, "Nothing accepted or installed. Use --accept to record this version.")
		}
		return 0
	case "tag":
		if len(pos) != 1 {
			return fail(errors.New("usage: dotagents tag ID --topics TOPIC [--scope home|project]"))
		}
		found := false
		for i, entry := range a.Config.Skills {
			if entry.ID == pos[0] {
				a.Config.Skills[i].Topics = splitGroups(*topics)
				if *scope != "" {
					a.Config.Skills[i].Scope = *scope
				}
				found = true
				break
			}
		}
		if !found {
			return fail(fmt.Errorf("unknown skill %s", pos[0]))
		}
		if err := a.Save(); err != nil {
			return fail(err)
		}
		fmt.Fprintln(out, "Updated topic/scope rules for", pos[0], "— installations unchanged.")
		return 0
	case "project":
		if len(pos) != 1 {
			return fail(errors.New("usage: dotagents project TOPIC --directory PATH [--targets codex,claude]"))
		}
		result, err := a.Project(ctx, pos[0], *directory, splitGroups(*targets))
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, result, fail)
		}
		fmt.Fprintf(out, "Installed %s into %s\nCatalog: %s\nTransaction: %s\n", strings.Join(result.Skills, ", "), result.Directory, result.Catalog, result.Transaction)
		return 0
	case "accept":
		if len(pos) != 1 || *from == "" {
			return fail(errors.New("usage: dotagents accept ID --from installed|tool"))
		}
		if err := a.Accept(ctx, pos[0], *from); err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, map[string]string{"id": pos[0], "accepted_from": *from}, fail)
		}
		fmt.Fprintln(out, "Accepted new snapshot for", pos[0], "— installations unchanged.")
		return 0
	case "ignore":
		if len(pos) != 1 || strings.TrimSpace(*reason) == "" {
			return fail(errors.New("usage: dotagents ignore PATH --reason TEXT"))
		}
		path := expandHome(a.Home, pos[0])
		rel, err := filepath.Rel(a.Home, path)
		if err != nil {
			return fail(err)
		}
		a.Config.Ignore = append(a.Config.Ignore, Ignore{Path: rel, Reason: *reason})
		if err := a.Save(); err != nil {
			return fail(err)
		}
		fmt.Fprintln(out, "Recorded ignore rule. Files unchanged.")
		return 0
	case "track-mcp":
		if len(pos) != 1 || *client == "" {
			return fail(errors.New("usage: dotagents track-mcp NAME --client claude|codex"))
		}
		if err := a.TrackMCP(*client, pos[0], splitGroups(*groups)); err != nil {
			return fail(err)
		}
		fmt.Fprintln(out, "Recorded MCP definition. Full contents stay in the private local snapshot; the catalog contains a redacted view. Native settings unchanged.")
		return 0
	case "define-mcp":
		if len(pos) != 1 || *client == "" || *definitionFile == "" {
			return fail(errors.New("usage: dotagents define-mcp NAME --client claude|codex --file JSON [--groups GROUPS]"))
		}
		if err := a.DefineMCP(*client, pos[0], *definitionFile, splitGroups(*groups)); err != nil {
			return fail(err)
		}
		fmt.Fprintln(out, "Saved portable MCP definition. Run mcp-plan to preview activation.")
		return 0
	case "plan":
		p, err := a.Plan()
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			if code := writeJSON(out, p, fail); code != 0 {
				return code
			}
			if len(p.Issues) > 0 {
				return 1
			}
			return 0
		}
		for _, action := range p.Actions {
			fmt.Fprintf(out, "%-9s %-22s %s\n", action.Kind, action.ID, action.Path)
		}
		for _, issue := range p.Issues {
			fmt.Fprintln(out, "BLOCKED:", issue)
		}
		if len(p.Actions) == 0 && len(p.Issues) == 0 {
			fmt.Fprintln(out, "Accepted skills are already installed as planned.")
		} else {
			fmt.Fprintf(out, "\nPlan revision: %s\nApply with: dotagents apply --revision %s\n", p.Revision, p.Revision)
		}
		if len(p.Issues) > 0 {
			return 1
		}
		return 0
	case "apply":
		if *revision == "" {
			return fail(errors.New("run plan first, then apply --revision HASH"))
		}
		txn, err := a.Apply(ctx, *revision)
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, map[string]string{"transaction": txn}, fail)
		}
		fmt.Fprintln(out, "Applied. Transaction:", txn)
		return 0
	case "rollback":
		if len(pos) != 1 {
			return fail(errors.New("usage: dotagents rollback TRANSACTION"))
		}
		if err := a.Rollback(pos[0]); err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, map[string]string{"restored": pos[0]}, fail)
		}
		fmt.Fprintln(out, "Restored transaction", pos[0])
		return 0
	case "mcp-plan":
		p, err := a.MCPPlan(ctx)
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			if code := writeJSON(out, p, fail); code != 0 {
				return code
			}
			if len(p.Issues) > 0 {
				return 1
			}
			return 0
		}
		for _, action := range p.Actions {
			fmt.Fprintf(out, "%-9s %-8s %s\n", action.Kind, action.Client, action.Name)
			for _, change := range action.Changes {
				fmt.Fprintln(out, change.Diff)
			}
		}
		for _, issue := range p.Issues {
			fmt.Fprintln(out, "BLOCKED:", issue)
		}
		if len(p.Actions) == 0 && len(p.Issues) == 0 {
			fmt.Fprintln(out, "Selected MCP definitions already match.")
		} else {
			fmt.Fprintf(out, "\nPlan revision: %s\nApply with: dotagents mcp-apply --revision %s\n", p.Revision, p.Revision)
		}
		if len(p.Issues) > 0 {
			return 1
		}
		return 0
	case "mcp-apply":
		if *revision == "" {
			return fail(errors.New("run mcp-plan first, then mcp-apply --revision HASH"))
		}
		txn, err := a.MCPApply(ctx, *revision)
		if err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, map[string]string{"transaction": txn}, fail)
		}
		fmt.Fprintln(out, "Published MCP settings. Transaction:", txn)
		return 0
	case "mcp-rollback":
		if len(pos) != 1 {
			return fail(errors.New("usage: dotagents mcp-rollback TRANSACTION"))
		}
		if err := a.MCPRollback(pos[0]); err != nil {
			return fail(err)
		}
		if *jsonOutput {
			return writeJSON(out, map[string]string{"restored": pos[0]}, fail)
		}
		fmt.Fprintln(out, "Restored MCP transaction", pos[0])
		return 0
	case "serve":
		return serve(ctx, a, *addr, out, fail)
	default:
		return fail(fmt.Errorf("unknown command %q; run dotagents help", command))
	}
}

func writeJSON(out io.Writer, v any, fail func(error) int) int {
	e := json.NewEncoder(out)
	e.SetIndent("", "  ")
	if err := e.Encode(v); err != nil {
		return fail(err)
	}
	return 0
}

func splitGroups(value string) []string {
	var result []string
	for _, s := range strings.Split(value, ",") {
		if s = strings.TrimSpace(s); s != "" {
			result = append(result, s)
		}
	}
	return result
}

// Keep flags usable after the item path without introducing a CLI dependency.
func interspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			pos = append(pos, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			continue
		}
		i++
		if i == len(args) {
			return nil, fmt.Errorf("%s requires a value", arg)
		}
		flags = append(flags, args[i])
	}
	return append(append(flags, "--"), pos...), nil
}

func printReport(out io.Writer, r Report, scan bool) {
	fmt.Fprintf(out, "%s · %s/%s · groups: %s\n\n", r.Machine.Hostname, r.Machine.OS, r.Machine.Arch, strings.Join(r.Machine.Groups, ", "))
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tITEM\tOWNER\tDETAIL")
	for _, f := range r.Findings {
		if !scan && (f.Status == "ok" || f.Status == "ignored" || f.Status == "inactive") {
			continue
		}
		owner := f.Owner
		if owner == "" {
			owner = "unknown"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", f.Status, f.Name, owner, f.Message)
	}
	w.Flush()
	for _, issue := range append(append([]string{}, r.Inventory.Issues...), r.MCP.Issues...) {
		fmt.Fprintln(out, "Could not inspect:", issue)
	}
	var parts []string
	for _, status := range []string{"ok", "unknown", "changed", "tool_changed", "missing", "unexpected", "unverified", "ignored", "inactive"} {
		if n := r.Counts[status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, status))
		}
	}
	fmt.Fprintf(out, "\n%s\nScanned %d physical skills and %d MCP definitions. Nothing changed.\n", strings.Join(parts, " · "), len(r.Inventory.Items), len(r.MCP.Entries))
}

func serve(ctx context.Context, a *App, addr string, out io.Writer, fail func(error) int) int {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fail(err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fail(errors.New("dashboard must bind a loopback IP, such as 127.0.0.1:7391"))
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fail(err)
	}
	actual := listener.Addr().String()
	handler := dashboard.Handler(func() (any, error) {
		fresh, err := Open(a.Repo, a.Home)
		if err != nil {
			return nil, err
		}
		return fresh.Report(ctx), nil
	})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != actual {
			http.Error(w, "Unexpected host", http.StatusForbidden)
			return
		}
		handler.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	fmt.Fprintln(out, "Dashboard: http://"+actual)
	fmt.Fprintln(out, "Read-only. Checks run on page load and when you press Refresh. Ctrl-C to stop.")
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		case <-done:
		}
	}()
	err = srv.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fail(err)
	}
	return 0
}
