# dotagents

Know what your coding agents are running—and what changed.

`dotagents` keeps a catalog of the skills and MCP connections you want on each
machine. It discovers things installed outside the catalog, saves the actual
skill contents you accept, and compares those files with both your current
installation and the output supplied by supported tools.

It runs on **macOS and Linux** as one Go binary. There is no JavaScript or Python
runtime to install. Checks run when you invoke them; there are no scheduled jobs
or terminal hooks. Unknown skills stay in place until you decide what to do.

## Start by looking

Build with Go 1.24 or later:

```sh
make build
./dist/dotagents scan
./dist/dotagents check
./dist/dotagents serve
```

Open the loopback URL printed by `serve`. The dashboard lets you search skills,
inspect ownership and file diffs, and review MCP connections. It is read-only.
Checks happen on page load and when you press **Check now**. Stop it with Ctrl-C.

Or save a report you can open without a running server:

```sh
./dist/dotagents export --output /tmp/dotagents-report.html
```

Reports include local paths and skill contents shown in diffs. Keep them private
unless you have reviewed what they contain. The exported HTML needs no network
connection or external assets.

The commands above use `dotagents.json` in the current directory. The public
catalog starts empty; your personal inventory need not be published. To create
a separate catalog, or inspect another home directory explicitly:

```sh
./dist/dotagents init --repo /path/to/my-agent-catalog
./dist/dotagents check --repo /path/to/my-agent-catalog --home /path/to/home
```

To avoid repeating the catalog path, you can set `DOTAGENTS_REPO` in your own
environment. An explicit `--repo` always wins. The application does not edit
your shell configuration or install a hook.

```sh
export DOTAGENTS_REPO=/path/to/my-agent-catalog
dotagents check
```

`--home` changes the filesystem being inspected; it does not impersonate another
machine. OS, architecture, hostname, and environment conditions still describe
the machine running the command. Examples below use `dotagents` on your PATH;
substitute `./dist/dotagents` when running from the checkout.

## Bring an installed skill into the catalog

First inspect `scan`, then adopt the path you actually want to track:

```sh
dotagents adopt "$HOME/.agents/skills/my-skill" --owner manual
```

This saves the skill's files, including supporting files and file modes, and
records the installed location. Git history and empty directories are not part
of that snapshot. It does not move or reinstall the skill.
The accepted content lives in `catalog/`; the declaration lives in
`dotagents.json`. Both can be versioned in Git after reviewing their contents.

Choose the owner deliberately:

| Owner | Meaning |
| --- | --- |
| `manual` | Your authored or deliberately maintained contents. |
| `skillshare` | A repository skill to publish through Skillshare links. |
| `td` | Compare with the supported generator in the installed Todoist CLI. |
| `jev` | Compare with the installed Jev bundle and ownership marker. |
| `native` | Record an agent/plugin-owned installation for inspection. |
| `tool:NAME` | Record another tool as the owner; unsupported output comparisons remain unverified. |

Detected ownership in `scan` is a hint, not an automatic decision. A lockfile's
source URL is evidence of origin, not proof that the files still match it.

To leave something outside the catalog intentionally:

```sh
dotagents ignore "$HOME/.claude/skills/synced" --reason "Managed by Claude account sync"
```

An ignore rule records the reason and leaves the files alone.

## See the difference before accepting it

For a tracked skill there are three separate versions:

1. **Accepted:** the exact files saved in the catalog.
2. **Installed:** what the agents can read on this machine.
3. **Current tool:** what the currently installed `td` or `jev` supplies, when
   the read-only adapter recognises its layout.

```sh
dotagents check
dotagents diff jev-cli
dotagents probe td
dotagents probe jev
```

An unsupported or changed tool layout produces a comparison failure, never an
assumed match. These probes do not execute the tool's CLI, install a skill, or
upgrade the tool. This matters because some CLIs refresh global skills before
dispatching even a seemingly local command.

After reviewing the differences, accept either the local edits or current tool
output explicitly:

```sh
dotagents accept jev-cli --from installed
dotagents accept jev-cli --from tool
```

Accepting changes the saved version; it does not change the installation.
Comparison with an uninstalled newer tool release is not implemented. `tdc`
also has no provider yet: use `--owner tool:tdc` to record its ownership without
claiming that its current generated output was compared.

## Decide which machines get what

Groups are calculated from conditions in `dotagents.json`. You do not select a
profile each time. A machine can belong to several groups.

```json
{
  "mac": { "os": ["darwin"] },
  "linux": { "os": ["linux"] },
  "work": {
    "any": [
      { "hostnames": ["work-*", "office-*"] },
      { "env": { "DOTAGENTS_ROLE": "work" } }
    ]
  }
}
```

This is the `groups` object, not a complete catalog. A valid starting catalog
with more examples is in [docs/dotagents.example.json](docs/dotagents.example.json).

Supported conditions are `os`, `arch`, hostname glob patterns, exact environment
values, paths that `exists`, and nested `all`, `any`, and `not` conditions.
Different fields on one condition must all match. An item's `groups` list
matches **any** named group; an omitted or empty list means **every machine**.
A skill's optional `when` condition must also match.

For example, after each tool has installed its skill at the paths below:

```sh
# No --groups: wanted on every machine.
dotagents adopt "$HOME/.agents/skills/todoist-cli" --owner td

# Only machines matching the work group; output comparison remains unverified
# until a safe tdc adapter exists.
dotagents adopt "$HOME/.agents/skills/tdc" --owner tool:tdc --groups work
```

The owner identifies the source of contents. Machine rules identify where they
belong. They are separate decisions.

For supported tools, you can record the desired skill before it is installed:

```sh
dotagents add-tool td --targets codex,claude
dotagents add-tool jev --targets codex --groups work
```

These commands inspect the installed tool, accept its current skill contents,
and record the intended destinations. They do not install the skill until you
review and apply a plan. The `td` and `jev` programs themselves must be installed
through their normal package managers; dotagents manages their skills, not the
tool binaries.

## Put a topic into a project

Topics group accepted skills independently of machine groups. Tag the skills
once, then install the topic wherever you need it:

```sh
dotagents tag cf-cli --topics cloudflare --scope project
dotagents tag durable-objects --topics cloudflare --scope project
dotagents project cloudflare --directory /path/to/project
```

The last command installs the selected accepted skills for Codex and Claude;
use `--targets codex` or `--targets claude` to narrow that selection. It creates
a project catalog at `.dotagents-project/`, copies the accepted snapshot objects,
and applies through the same backup and rollback flow. An untracked skill with
the same destination name blocks installation instead of being overwritten.
Native-owned skills cannot be republished this way.

`--scope project` keeps a skill out of future global installations. Tagging alone
does **not** remove anything. The next global `plan` can propose removing its
tracked global copies, and `apply` performs that reviewed removal only when
their contents still match the accepted version. Local edits, native ownership,
incomplete discovery, or dependent unknown links block removal. Unknown skills
are never removed just because they are outside the catalog.

Inspect or restore the project using its catalog and root:

```sh
dotagents check --repo /path/to/project/.dotagents-project --home /path/to/project
dotagents rollback TRANSACTION_ID \
  --repo /path/to/project/.dotagents-project --home /path/to/project
```

The project catalog and its accepted objects can be versioned after review.
Its private `.dotagents/` directory is ignored. Generated links are specific to
the checkout and should be recreated with `plan` and `apply` on another machine.
Project MCP definitions use the same track/define/plan/apply commands with the
project's `--repo` and `--home`. Claude uses the project's `.mcp.json`; Codex uses
its `.codex/config.toml`. A topic command installs skills; it does not implicitly
activate MCP connections.

## Preview, back up, then apply

To add one repository skill, fetch it at a full Git commit SHA. This uses
Skillshare in an isolated staging directory and records accepted contents
without exposing them to an agent yet:

```sh
dotagents fetch cloudflare/skills --skill cf-cli \
  --ref 6eb6c781aa9b6438e0c7307bd102788a75fe1a6d \
  --targets codex,claude
```

The commit above is an example pin, not a promise of the latest release.
`--groups` can limit which machines receive the skill. For an existing repository
skill, inspect a newer upstream version explicitly:

```sh
dotagents refresh cf-cli
dotagents refresh cf-cli --ref FULL_COMMIT_SHA --accept
```

`refresh` resolves the latest GitHub commit unless you pass `--ref`, fetches that
exact version, and shows the diff. Without `--accept`, neither the catalog nor
the installation changes. With `--accept`, the new snapshot and source pin are
recorded; installation still requires a separate reviewed plan.

```sh
dotagents backup
dotagents plan
dotagents apply --revision HASH_PRINTED_BY_PLAN
```

`plan` previews the changes and gives you a revision. `apply` requires that exact
revision, checks for intervening changes, and takes a verified backup before
writing. Conflicts are reported instead of silently replacing untracked data.
The transaction ID printed by apply can be used with:

```sh
dotagents rollback TRANSACTION_ID
```

Rollback refuses to overwrite changes made after the transaction. A full backup
is also available under `.dotagents/backups/`, with an archive hash and receipt.
The archive contains the repository and the home-level skill/settings paths
covered by the backup implementation, preserving symlinks. It is not a backup
of your entire home directory.

**Keep `.dotagents/` private.** It contains backups, transaction data, and raw MCP
definitions that may include credentials. It is ignored by Git. Never force-add
it to this public repository.

## MCP connections

The inventory reads global Claude and Codex MCP definitions without connecting
to servers. To record one:

```sh
dotagents track-mcp my-server --client codex --groups work
dotagents check
```

The catalog contains a fingerprint and a redacted definition. The exact accepted
definition stays in `.dotagents/private/mcp/` with restrictive permissions.
Changes—including credential changes—can therefore be detected without putting
raw settings into a report or Git.

Private snapshots are deliberately not distributed by a public Git clone. A
second machine needs its credentials provisioned separately. Tracking a
connection does not authenticate it or start its server.

For a definition intended to travel with the catalog, author a JSON definition
that contains no credentials and record it explicitly:

```sh
dotagents define-mcp my-server --client codex --file portable-mcp.json --groups work
dotagents mcp-plan
dotagents mcp-apply --revision HASH_PRINTED_BY_MCP_PLAN
```

`define-mcp` stores that authored definition in the versioned catalog. Common
secret fields are rejected, but this is not a universal secret detector: review
the file before storing or publishing it. Do not feed a raw private settings
file into this command.

MCP publication uses Skillshare in isolated staging, preserves unrelated native
settings, and verifies the reviewed revision before writing. Private backups are
kept for rollback:

```sh
dotagents mcp-rollback TRANSACTION_ID
```

Changing an MCP rule's groups lets `mcp-plan` propose activation or removal on
the appropriate machines. Later edits block rollback rather than being lost.

## What each piece does

- **dotagents** owns the catalog, machine rules, discovery, accepted snapshots,
  comparisons, reviewable plans, and backups.
- **Skillshare** is used for its supported skill fetching/linking and MCP staging. It
  is a separate executable needed for those operations, not for inventory,
  snapshot comparisons, or viewing reports.
- **Tool adapters** inspect supported `td` and `jev` installations without
  running their installers. Package layouts outside the recognised forms are
  reported as unverified. `probe --path /installed/package` can select an
  explicit installation; catalog entries use `provider_path` for that purpose.
- **Chezmoi is not required.** You can use it to distribute this configuration
  as part of an existing dotfiles workflow, but it does not own the same live
  files as another manager.

The binary itself needs no Go, Node, or Python installation at runtime. A tool
must still be installed if you want to inspect its bundled skill. Building
requires Go; repository fetching may also require Skillshare and its own
dependencies.

## Scope and limitations

Default skill discovery covers `~/.agents/skills`, `~/.codex/skills` (including
`.system`), and `~/.claude/skills` (including nested synced skills). Symlinked
copies are deduplicated while retaining their observed locations. Extra skill
roots can be configured. Scan errors and broken links remain visible.

Native plugin declarations are included. Claude's enabled settings and installed
plugin registry identify the user-scoped skill roots to inspect. Codex plugin
settings are reported as declarations; their runtime-managed active versions
are not guessed from cached directories. Plugin caches alone are never proof
that a plugin is active.

Account-side integrations and arbitrary project folders are not silently
inferred. Select a project's catalog explicitly to inspect it. The report lists
its scan scope; a clean result is only a statement about that scope. MCP support
currently covers Claude and Codex. Tool adapters are deliberately narrow; more
can be added without changing who owns those skills.

Exit codes are `0` for success/clean, `1` for findings, and `2` for a command or
configuration failure. `--json` is available for automation. Nothing schedules
that automation for you.

## Build and verify

```sh
make build       # dist/dotagents for this machine
make test        # Go tests with the race detector
make vet
make cross       # macOS/Linux × arm64/amd64; checksums in dist/SHA256SUMS
```

Cross builds disable CGO. The Go module dependencies are compiled into the
binary. GitHub CI tests macOS and Linux and uploads the four binaries as workflow
artifacts; the new binary workflow does not publish releases on pushes.
After downloading a binary, make it executable with `chmod +x` before running it.

Implementation details are in [docs/design.md](docs/design.md).

## Previous installation

The existing `bin/setup`, `bin/doctor`, `bin/restore-skills`, public installer,
and legacy lockfile remain the previous live installation path. They have not
been repurposed to run the new CLI. Do not use the public installer expecting
this new workflow.

The previous README is preserved in [docs/legacy.md](docs/legacy.md). The legacy
release workflow still applies to its own lockfile; the new binary CI is
separate. These pieces can be retired after the migration is verified.
