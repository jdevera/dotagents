# Implementation

`dotagents` separates the record of intent from the files agents currently read.
The core application is Go. Its CLI and embedded local web view share the same
report model. The only Go library dependency is a TOML parser, compiled into the
binary for reading native Codex configuration.

## Boundaries

| Package | Responsibility |
| --- | --- |
| `cmd/dotagents` | Process entry point and cancellation. |
| `internal/app` | Catalog validation, machine conditions, report composition, CLI, reviewable plans, transactions, and backup. |
| `internal/skills` | Filesystem discovery, complete skill trees, immutable content snapshots, and diffs. |
| `internal/providers` | Read-only inspection of supported installed tool packages. |
| `internal/integrations` | Optional pinned audit fetches and manual MCP staging. |
| `internal/mcp` | Native MCP parsing, redaction, and fingerprints. |
| `internal/plugins` | Native plugin declarations and evidenced active Claude skill roots. |
| `internal/dashboard` | Embedded local dashboard and standalone HTML export. |

The inventory and comparison packages do not install tools or contact MCP
servers. The dashboard invokes reports and copies commands; it has no mutation
endpoint. Only explicit CLI mutation commands change catalog or live state. Native
Skillshare app/CLI changes are allowed directly; accepted audit snapshots do
not gate or automatically follow those changes.

## Intent and accepted contents

`dotagents.json` is the versioned declaration. It records source, owner,
locations, group selection, and the accepted content snapshot for each skill.
A skill ID is stable within the catalog. A path is not treated as proof of who
owns its contents.

`catalog/` stores content-addressed file objects and manifests. The accepted
version contains actual bytes rather than only a checksum or upstream URL.
Manifests preserve relative filenames, file modes, and supported link records.
Objects are verified when loaded; a corrupted object produces an error.

Adoption and acceptance are explicit writes to this record. Neither implicitly
updates the installed skill. Hand-authored or private skill contents require
review before committing a catalog to a shared repository.

## Machine selection

Rules use OS (`darwin` or `linux`), architecture, hostname globs, environment
values, and filesystem existence. Conditions compose with `all`, `any`, and
`not`. Different fields within a condition are conjunctive. A skill's group
list is disjunctive: any matching group selects it. No groups means everywhere.
An optional skill-specific `when` condition is an additional requirement.

Rules evaluate against the running machine. `--home` selects a filesystem root;
it does not simulate a hostname, OS, or environment. Unknown groups and malformed
conditions are rejected instead of silently selecting nothing.

## Three comparisons

The report can distinguish accepted→installed, accepted→current-tool, and
installed→current-tool differences. It does not infer that an installed file is
unchanged from a package version number.

The `td` adapter recognises a specific generator expression and statically
decodes its literal inputs. It never starts the CLI, JavaScript, or an installer.
The `jev` adapter reads its bundled files and recognised installer ownership
marker. Unknown layouts fail visibly. Both compare the currently installed tool,
not the latest remotely available version.

`native` records agent-owned contents without taking over their installation.
`tool:NAME` records other tool ownership even when no safe output adapter exists;
such a comparison is explicitly unverified. General shell commands are not a
provider configuration mechanism.

## Skillshare integration

Skillshare owns its ordinary stable library and global configuration, so its
native app and CLI operate on the same data. `skillshare-plan` reports the
connection, missing upstream imports, target selection, source differences,
and migration collisions. `skillshare-connect --phase prepare` initializes and
fills that library through native commands. It does not sync live targets or
accept new audit contents. The source must be outside agent discovery roots,
otherwise target filtering cannot limit exposure.

The `policy` phase applies calculated machine selection as native target
exclusions, recording only the exclusions dotagents owns. Unrelated target
settings and user filters remain native state. This phase does not sync either.
A revision binds each operation to the plan reviewed immediately before it.
Existing agent directories, foreign links, unsupported native policies, and
pending sync are not silently called a successful migration.

`plan`/`apply` delegate all Skillshare-owned skills rather than staging and
publishing a second copy. New native changes remain observable against the
accepted snapshots. Project topics likewise use native project registration,
backup, and sync rather than a second project catalog.

Optional `fetch`/`refresh` commands still request pinned GitHub contents through
an isolated Skillshare library for audit comparisons. They verify native
provenance and file hashes, but never update the permanent native library.
This path is for explicit baseline inspection, not everyday installation.

The desktop app has its own automatic update checks. The browser UI avoids that
desktop watcher but refreshes visible status and fetches Git remotes on its Git
Sync page. Dotagents does not schedule checks in either tool.

## Preview and transaction safety

For manually maintained and tool-owned contents, a plan's revision covers the
catalog, selected machine, observed files, and proposed actions. Apply rechecks that revision and refuses stale plans. It
prepares outputs and verified backups before replacing live paths. Publication
uses temporary siblings and rename operations; it does not follow a changed
destination blindly.

Transaction records and evidence stay under `.dotagents/transactions/`.
Rollback checks the post-apply fingerprints before restoring an original path,
so later user edits are not overwritten. The separate `backup` command makes a
permission-restricted archive of the repository and affected home-level skill
and configuration paths, then reads it back to verify file hashes.

Old transaction storage may still be the destination of links created before
the native handoff. It cannot be discarded until those installations have been
migrated and inspected. New Skillshare-owned publication uses its stable library.

## Project topics

Topics are named selections of Skillshare-owned skills. The `project` command
checks machine eligibility, finds their current native-library contents, and
registers selected target agents and include filters as a machine-local native
Skillshare project. It previews for collisions, asks Skillshare to back up target
folders, syncs through Skillshare, and verifies the expected links. Existing
native selections are preserved. Native custom modes, naming, or managed agent
configurations that the helper cannot safely extend require direct Skillshare
configuration. Native project errors remain visible.

No `.dotagents-project` directory or additional transaction is created. A legacy
project catalog blocks the new helper until explicitly migrated. Shared team
project configuration belongs to Skillshare's native project workflow; this
helper registers personal folders outside the project repository.

Project-only scope excludes a skill from home selection when policy is applied.
It does not itself remove legacy installations. Skillshare's native preview and
sync determine the actual target changes after migration conflicts are resolved.
A topic operation does not activate MCP connections.

## MCP and private state

Global Claude JSON and Codex TOML are parsed without starting servers. The full
canonical definition is fingerprinted so changes to hidden fields are still
detected. The catalog receives a redacted representation. Raw accepted
definitions are stored privately under `.dotagents/private/mcp/` and are not
included in Git or dashboard responses.

A clone of the public catalog cannot restore secrets it does not contain.
Credentials and private snapshots need a separate private transfer/provisioning
mechanism. `define-mcp` supports explicitly authored portable definitions in the
catalog, rejecting common secret fields. It cannot prove that arbitrary user
content is secret-free; the author must review it.

MCP records carry optional ownership. `skillshare` and `native` records are
always audited but delegated by `mcp-plan`/`mcp-apply`; old accepted bytes never
replace their owner's changes. An empty or `manual` owner retains the explicit
manual publication flow: stage through Skillshare, verify native output,
preserve unrelated settings, and store private transaction backups. Track and
define operations preserve an existing owner unless explicitly changed. The
application does not claim to implement a secret-distribution system.

## Verification boundaries

The scan reports its roots and errors. It includes symlink locations and nested
home-level skill folders, plus enabled Claude plugin skill roots evidenced by
the user-scoped installed registry. Codex plugin declarations remain declarations;
their runtime-managed active versions are not inferred from cache. Projects are
explicitly selected. Remote account integrations and arbitrary harnesses require
additional support.

No check is scheduled automatically. The local server binds to loopback and
rejects unexpected Host and cross-origin requests. Exported HTML includes its
data and assets, with no network dependencies. Local reports can contain paths
and skill text, so exports should be treated as private until reviewed.
