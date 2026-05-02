## Workflow

- Read this guide end-to-end at the start of a task. Re-skim when major
  decisions arise or requirements shift.
- Treat git status and diffs as read-only. Never revert changes that may
  represent in-progress work.
- Plan against existing patterns in the codebase before reaching for
  external references.
- Surface meaningful trade-offs to me before committing to one.
- Don't push, force-push, open or close PRs, comment on GitHub, or
  send messages on my behalf. Ask first. Local commits are fine.
- When asked to plan, plan only. No edits, no commits, no commands
  that change state until I confirm.

## Code quality

- Match surrounding style. Reuse existing utilities and helpers before
  inventing new abstractions.
- Fix root causes, not symptoms. No band-aids.
- Delete unused code instead of leaving it. No backwards-compatibility
  shims unless I ask for them.
- No breadcrumb comments where code used to live.

## Communication

- Be direct and technically honest. Skip filler and praise.
- Disagree when you think I'm wrong, and explain why.

## Tools

- Prefer `gh` for GitHub operations.
- Reach for `git log` / `git blame` when historical context would help.
