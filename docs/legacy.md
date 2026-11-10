# dotagents

> Hi, future me. The agents have amnesia. This is the box of notes I
> hand them every morning.

Personal harness for AI coding agents. The repo holds the hand-managed
`AGENTS.md` and the [`skills`](https://skills.sh) CLI's lockfile. Every
lockfile change triggers a CI build that resolves each recorded source
into actual files and publishes the result as a release tarball: a
hermetic point-in-time snapshot of agent state. Those snapshots are
the preservation layer; the whole setup can be bootstrapped from one
of them.

## Install

```bash
sh -c "$(curl -fsLS agents.jdevera.casa/install)"
```

That clones this repo into `~/.agents`, runs `bin/setup`, restores every
skill from the lockfile, and finishes with `bin/doctor`. Requires `git`
and `npx` on the host; bails before changing anything if `~/.agents`
already exists.

### Manual install / fallback

If the vanity domain is unreachable (DNS, expired registration, world
ending), the same flow works by hand:

```bash
git clone git@github.com:jdevera/dotagents.git ~/.agents
cd ~/.agents
./bin/setup                          # symlinks into ~/.claude/ and ~/.codex/
./bin/restore-skills                 # restores every skill from .skill-lock.json
./bin/doctor                         # verify
```

`bin/restore-skills` is a thin wrapper that re-issues `npx skills
add … -g` per lockfile entry. It exists because the `skills` CLI has
no command to restore global skills from `.skill-lock.json` yet (see
[#1](https://github.com/jdevera/dotagents/issues/1) for context and
[vercel-labs/skills#743](https://github.com/vercel-labs/skills/pull/743)
for the upstream fix in flight). When that PR ships, the wrapper goes
away and this line becomes `npx skills experimental_install -g`.

`bin/doctor` checks for the things this repo assumes are present
(`gh`, a way to run `skills`, the symlinks `bin/setup` creates, the
lockfile) and prints a remediation hint for anything missing. It only
inspects state, so the order of `setup` / `restore-skills` / `doctor`
doesn't matter; doctor just reports whichever pieces aren't in place
yet.

## What lives here

| Path                | Role                                                      | Managed by |
|---------------------|-----------------------------------------------------------|------------|
| `AGENTS.md`         | Shared base instructions, symlinked into `~/.claude/CLAUDE.md` and `~/.codex/AGENTS.md` | hand |
| `bin/setup`         | Creates the symlinks above (and links `skills/*` into `~/.claude/skills/`) | hand |
| `bin/doctor`        | Verifies the install                                      | hand       |
| `bin/restore-skills` | Restores skills from `.skill-lock.json` (workaround, see issue #1) | hand |
| `.skill-lock.json`  | Per-skill source manifest (URL + commit SHA + timestamps) | `skills` CLI |
| `shelf.json`        | Deactivated skills                                        | `skills` CLI |
| `skills/`           | Skill files (only present after extracting a release tarball) | CI |
| `site/`             | Installer landing page + bootstrap script served at `agents.jdevera.casa` | hand |

Skill **files** are not committed in git, only the lockfile is. CI
materialises them into `skills/` as part of every release tarball, so
extracting a release gives you a fully populated `~/.agents/skills/`
ready to be linked into `~/.claude/skills/` by `bin/setup`.

## Why clone to `~/.agents`?

The `skills` CLI's default global lockfile path is
`~/.agents/.skill-lock.json`. Cloning this repo to `~/.agents/` puts
the lockfile inside the repo automatically. No symlinks, no env vars.
As a bonus, `~/.agents/skills/` is a path Pi and Gemini CLI
auto-discover, which makes future expansion painless.

## Release snapshots

A GitHub Actions workflow (`.github/workflows/release.yml`) fires on
every push to `main` that touches `.skill-lock.json`. It resolves each
recorded source into actual files (rather than the link records git
tracks), bundles the repo's hand-managed content alongside, and
publishes the result as a release tarball.

**Why this exists**: preservation. Releases are a hermetic record of
what agent state looked like at every lockfile change. They let you:

- Boot a new machine from one extract.
- Inspect or recover a past version without re-cloning upstreams.
- Survive an upstream disappearing (the internet is forever, until it
  isn't). The snapshot still has the files.

**What's in a snapshot**:

- The repo's tracked content (everything except `.git` and `.github`).
- Every skill resolved from the lockfile, at its recorded ref.
- Excluded as build-output noise: `node_modules`, nested `.git`
  directories, and `.DS_Store`.

The lockfile is the only input today. To preserve anything outside it
(e.g. files I dropped directly into `~/.claude/`), I extend
`.github/scripts/build-bundle.sh` to copy those paths into the staging
dir before it tars.

**Versioning** is `YYYY-MM-DD.N` where N is the number of prior
lockfile-touching commits on the same calendar day. The first release
of a day is `.0`, the next is `.1`, and so on. (If you commit several
times offline and push them all at once, the day may start at a
non-zero N. That's fine; uniqueness and ordering are what matter.)
Releases stay forever.

## Should I add a skill here?

No. Skills I author or fork live in
[`jdevera/agent-skills`](https://github.com/jdevera/agent-skills); this
repo only tracks the lockfile that records what's installed. To add a
new skill (say, `commit-message`):

1. Author or fork it in `agent-skills` under
   `skills/commit-message/SKILL.md`, commit and push.
2. From `~/.agents`:

   ```bash
   npx skills add jdevera/agent-skills --skill commit-message -g
   ```

   That clones the latest `agent-skills`, copies the skill into
   `~/.claude/skills/commit-message/`, and writes the source URL +
   commit hash into `.skill-lock.json`.
3. Commit and push the lockfile change. CI builds a new release
   tarball containing the resolved skill files.

## Day-to-day

```bash
# Add a skill (also writes to .skill-lock.json)
npx skills add jdevera/agent-skills -g
npx skills add owner/upstream-repo -g

# Pull upstream updates for installed skills
npx skills update

# Inspect what changed in the lockfile, then commit and push
cd ~/.agents
git diff
git commit -am "Add typescript-code-review fork"
git push
```

## References

- [Agent Skills specification](https://agentskills.io/specification)
- [`skills` CLI](https://github.com/vercel-labs/add-skill) ([npm](https://www.npmjs.com/package/skills))
- [agents.md](https://agents.md/): cross-vendor `AGENTS.md` convention
- [Claude Code skills docs](https://code.claude.com/docs/en/skills)
- Sibling repo: [`jdevera/agent-skills`](https://github.com/jdevera/agent-skills) (authored + vendored skills)

Prior art: [`goncalossilva/.agents`](https://github.com/goncalossilva/.agents),
[`PaulRBerg/dot-agents`](https://github.com/PaulRBerg/dot-agents),
[`fgrehm/dot-ai`](https://github.com/fgrehm/dot-ai) (archived).
