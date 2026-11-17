# The per-project -infra repo (`infra-*` commands)

Status: implemented. They are the git logic of
sandbox-infrastructure-template's `shell_utils.sh` (`cloneInfraRepo`,
`gitMergeInfraMain`, `gitMergeOriginMain`, `gitCommit`,
`gitSnapshotCommit`, `gitPushInfra`, `persistInfraRepo`), moved to Go
(sandbox-infrastructure-template#190, #160, #143). The template's scripts
call them.

## Why

Every project built from the template has an `<id>-infra` GitHub repo: an
output copy of what was deployed. ui-core's project archive is the source
of truth. The rules for keeping that repo right grew to about 500 lines of
shell (merges that must not run over a rebase, a snapshot that never
pushes a credentials file, exec bits on a filesystem where everything is
0777). In Go they are tested as ordinary unit tests against real git repos
(`internal/infrarepo/*_test.go`), and read in one place.

mars runs the `git` CLI (the image ships it), not a Go git library: the
rules are written in git's own terms (`merge`, `reset --mixed`, `ls-files
--ignored`, `update-index --chmod`), and hooks, credential helpers and
`core.sshCommand` work as they do for git.

## Opt-in

No other command uses them, and they read no mars config. The `<env>`
argument mars needs before each command is not used: the template passes
`default`.

The project root is `--root`, else `$MARS_PROJECT_ROOT`, else the current
directory. The `-infra` repo's URL is the `repo_clone_ssh_url` value in the
project's `tf/auto-vars/*.json` (then `secrets/*.auto.tfvars.json`), as
the template's `getTfVar` reads it. With `GITHUB_GIT_TOKEN_FILE` or
`GITHUB_TOKEN_FILE` naming a readable file, git uses HTTPS with that
GitHub App token through a credential helper (the token never goes into
`.git/config`); without one, the deploy key `secrets/infra_deploy_key.pem`
over SSH.

## Commands

| Command | What it does | Exit codes |
|---|---|---|
| `mars <env> infra-clone` | A tree without `.git` (an extracted archive) gets the -infra repo's git state; the worktree is left as it is, and the checkout is marked `luther.infraSnapshotTree` | 0 always (warns) |
| `mars <env> infra-merge [--abort-on-conflict]` | Merge `infra/main` into the project | 0 merged or nothing to do; 1 refused or failed; 2 not merged on purpose |
| `mars <env> infra-pull` | `pull.sh`: merge `origin/main` when there is an origin remote, then `infra/main` (rc 2 counts as 0) | 0, 1, or a failed fetch's code |
| `mars <env> infra-commit [message]` | `commit.sh`, `destroy.sh`: commit the tree | 0 committed or nothing to commit; 1 refused or failed, nothing committed |
| `mars <env> infra-snapshot [message]` | Commit the deployed tree as one commit on top of `infra/main` | 0, or 1 refused or failed |
| `mars <env> infra-push` | `git push infra HEAD` | 0 always (warns) |
| `mars <env> infra-persist` | Clone if needed, snapshot, push (the custom-stack apply) | 0 always (warns) |

Commands that "warn" print a greppable `[git-infra] WARNING:` line: a git
problem must never turn a deploy that already applied into a failure
(#143), and must never be silent either.

### infra-merge

Run before Terraform (`apply.sh`, with `--abort-on-conflict`) and by
`pull.sh`, so a developer's stale checkout gets teammates' edits first.

- 0: merged, nothing new, no `.git`, no infra remote, or no `infra/main` yet.
- 1: a merge, rebase, cherry-pick, revert or paused multi-commit
  cherry-pick/revert (`sequencer/`) is in progress: refused and never
  aborted, and the message names the command that ends it. Also: the git
  state cannot be read (fail closed), `infra/main` cannot be fetched, a
  shallow clone cannot be deepened, `merge-base` fails other than with
  "no common ancestor", or the merge fails. With `--abort-on-conflict` a
  failed merge is aborted, so no `MERGE_HEAD` or conflict markers stay for
  the snapshot to push; without it the conflict stays to resolve.
- 2: the checkout is marked `luther.infraSnapshotTree`, `HEAD` is unborn, or
  the histories share no commit (ui-core squashes the project history on
  every dispatch). Checked before any fetch for the mark. The apply goes on;
  its snapshot lands on top of `infra/main`.

`refs/heads/main` is fetched by its full name, so a tag named `main` never
wins.

### infra-snapshot

1. Refuses (1) any operation in progress, before it touches the index.
2. Fetches `infra/main`, records the project index's 100755 paths, and runs
   `git reset --mixed` onto it: HEAD and the index move, the worktree (the
   deployed tree) stays. Not `--soft`: a squashed tree that tracks the
   ignored credentials file would commit it.
3. Stages the tree (below), drops the template-only paths
   (`.github/dependabot.yml`), and commits.
4. Clears `luther.infraSnapshotTree` once HEAD sits on `infra/main`. With no
   `infra/main` yet it sets `luther.snapshotPending`, and the push that
   creates `infra/main` from this snapshot clears the mark. With no infra
   remote the mark stays.

The snapshot is exactly the deployed tree: a file only `infra/main` has is
removed from the tip (it stays in history).

### Staging rules (commit and snapshot)

- Every tracked file that `.gitignore` ignores is untracked first (`git rm
  --cached`, literal pathspecs); the file stays on disk. `.gitignore` does
  not stop `git add -A` from committing new versions of a tracked file, and
  the old template tracked the credentials file (#160). A failure to list
  or untrack stops the commit (fail closed).
- `git add -A` with `core.fileMode` off: an archive or mount can extract
  everything as 0777.
- Exec bits are set, never removed: a path new against the base commit that
  is executable on disk and starts with `#!` gets 100755; in a snapshot,
  every regular file the project index had at 100755 gets it again. Other
  paths keep their index mode, so a 644 to 755 change of an existing script
  made on disk only is not recorded (use `git update-index --chmod=+x`).
- A failed listing of index modes or new paths fails the staging; the shell
  ignored those failures and could drop an exec bit silently.

### infra-commit

Refuses (1) unresolved conflicts, a rebase, a cherry-pick or a revert; a
resolved merge is concluded, even when its result equals HEAD. A leftover
`sequencer/` without `CHERRY_PICK_HEAD` or `REVERT_HEAD` does not block it.
It keeps the template-only paths. `destroy.sh` maps its 1 to 75
("destroyed, but not committed").

## Known limits

- A detached HEAD: merge and snapshot work on the detached commit, and
  `git push infra HEAD` fails (warned, not fatal).
- A template clone used as a project tree can get a modify/delete conflict
  on `.github/dependabot.yml` when it merges `infra/main` after a snapshot;
  resolve it with `git rm`.
- `infra-clone` writes `<root>/.git` with `git clone --separate-git-dir`;
  a failed clone removes it (it did not exist before).

## Tests

`internal/infrarepo` runs each command on temp repos with real git: a bare
repo stands for the GitHub -infra repo. Failures git cannot be made to
produce (a failing `merge-base`, `fetch --unshallow`, `add`, `rm`,
`ls-files`, `rev-parse --git-path`) come from a runner that fails the
matching command, as the shell tests' PATH shims did. Each test names the
case of the template's `tests/test-infra-push.sh` it ports.
`internal/cli/infra_test.go` checks the wiring and exit codes.
