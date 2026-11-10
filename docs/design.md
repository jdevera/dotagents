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
| `internal/integrations` | Skillshare fetching in isolated staging directories. |
| `internal/mcp` | Native MCP parsing, redaction, and fingerprints. |
| `internal/plugins` | Native plugin declarations and evidenced active Claude skill roots. |
| `internal/dashboard` | Embedded local dashboard and standalone HTML export. |

The inventory and comparison packages do not install tools or contact MCP
servers. The dashboard invokes reports and copies commands; it has no mutation
endpoint. Only explicit CLI mutation commands change catalog or live state.

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

Fetching requests one skill at a full Git commit SHA in a temporary Skillshare
library with no targets. Configuration, cache, state, and data paths are isolated;
the process does not change `HOME`. The result must resolve to the requested
commit and match Skillshare's recorded file hashes before it can be accepted.

Publishing Skillshare-owned skills materialises accepted contents in transaction
storage. Skillshare creates links in staging targets. The application verifies
the staged links and contents before publishing them to the selected live
locations. This avoids relying on the copy-mode drift behaviour found during
evaluation. Skillshare is an optional executable dependency for these explicit
operations; normal discovery and comparison do not need it.

## Preview and transaction safety

A plan's revision covers the catalog, selected machine, observed files, and
proposed actions. Apply rechecks that revision and refuses stale plans. It
prepares outputs and verified backups before replacing live paths. Publication
uses temporary siblings and rename operations; it does not follow a changed
destination blindly.

Transaction records and evidence stay under `.dotagents/transactions/`.
Rollback checks the post-apply fingerprints before restoring an original path,
so later user edits are not overwritten. The separate `backup` command makes a
permission-restricted archive of the repository and affected home-level skill
and configuration paths, then reads it back to verify file hashes.

Do not delete transaction storage while an installed Skillshare link points into
its library. It is operational state, not a disposable cache.

## Project topics

Topics are named sets of skills. Machine groups still control eligibility;
topics control which set is installed into a project. A `project` command copies
the selected accepted snapshots into `.dotagents-project/`, creates target
locations for the requested harnesses, and applies those entries transactionally.
It rejects collisions with untracked project skills.

Project-only scope excludes a skill from home-level installations. Scope changes
are declarations, not silent uninstall operations. A subsequent reviewed plan
can remove tracked out-of-scope skills only when they match their accepted
contents and no dependent unknown links, native ownership, or incomplete scan
make removal unsafe. Unknown installations remain untouched.

Project MCP settings are read from Claude's `.mcp.json` and Codex's
`.codex/config.toml`. They use the same explicit MCP plan/apply/rollback flow;
installing a skill topic does not implicitly activate servers.

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
content is secret-free; the author must review it. MCP publication stages through
Skillshare, verifies native output, preserves unrelated settings, and stores
private transaction backups. The application does not claim to implement a
secret-distribution system.

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
