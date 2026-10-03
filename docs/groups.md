# Groups and runners

A group is who works a stage's beads: a pool of agents, a code step, or
humans. Its runner says how work starts; routes (`yardr route add`) send a
stage's beads to it. What a group's settings are, and where they are kept,
is in [configuration.md](configuration.md).

## Groups, interactively

`yardr group add` with no name opens a form: pick an existing group to edit or
create one, choose the runner, and for herdr the agent (Claude Code, Codex,
Grok, Kimi, opencode, pi, or any other herdr agent), a model read live from
that agent (press `/` to search long lists), and its permission flags. With a
name, `yardr group add <name> --runner … --set …` stays scriptable.

## Models

A herdr group's `model` goes to its agent as `--model`; a bead's
`--meta model=…` replaces it for that bead, in every herdr group that works
it. Nothing but the agent's provider knows whether a model may be used: a
name the agent lists may be refused for the account, and one it does not
list may run. An agent given a model it may not use still starts; the
refusal comes with its first request, in its pane, and it then sits idle.
An agent may also run another model than the one asked for without saying
so. The running session is the only place the truth shows, so yardr checks
there, not when the model is set: no command asks an agent about a model.

**The model the agent reports.** Once a bead session's agent has answered
its first prompt, the daemon reads the model the agent itself reports and
keeps it on the session's handle as `model_reported`, next to `model_asked`
(the bead's `metadata.model`, else the group's `model`; absent when neither
is set): `yardr session list --json` shows both. The source is the agent's
own session file, found by the session id herdr reports for the pane:

| kind | source |
|---|---|
| `claude` | `~/.claude/projects/<dir>/<session id>.jsonl` (or under `CLAUDE_CONFIG_DIR`): the `model` of the first assistant message, which is what the API answered with |
| `codex` | `~/.codex/sessions/<y>/<m>/<d>/rollout-<time>-<session id>.jsonl` (or under `CODEX_HOME`): the `model` of the first `turn_context` |
| any other | none known: nothing is recorded and nothing compared |

The daemon must run as the user whose agents these are, on their machine,
as it does for herdr anyway. An agent is asked on each pass until it has
answered, for the session's first 15 minutes.

**`session_model_mismatch`.** When the reported model is not the one asked
for, one event per session says so at once, with `asked`, `reported`,
`group` and `stage`. Names are compared by their words and numbers, case and
punctuation aside, and match when all of one name's are among the other's:
`claude-opus-5-5` matches `Opus 5.5` and `claude-opus-5-5-20260801`, an
alias such as `opus` (or `opus[1m]`) matches any Opus, and `claude-opus-5-5`
does not match `claude-fable-5-1` or `claude-opus-5-1`. The session is left
running: the mayor decides whether the work of the other model stands.

**`session_stalled`.** An agent that was given its
first prompt, was idle every time the daemon looked since, and has left
nothing behind (no note or move on its bead, no commit and no change in its
worktree) for the group's `stall` (default `2m`) is reported at once, not at
the `idle` limit: one `session_stalled` event per session, on the bead,
with `model`, `group`, `stage` and `tail`, the last 20 lines of the agent's
pane that say anything, where its error is. It is in a new resident's
default `--on`, as `session_model_mismatch` is, so both reach the mayor's
inbox ([subscriptions.md](subscriptions.md#--on)). An agent seen working or
blocked since its prompt is not judged, however long it idles later, and a
prompt held back by a startup question counts from its delivery. Only the
daemon that sent the prompt judges a session; one that replaced itself
since leaves it to `idle`.

The stalled session is kept, as an idle one is. While it sits idle the
group starts no further session with the same model (a bead with another
`--meta model` still starts), since each would stall alike and hold a slot.
The group goes on when the model is changed (`yardr group add` again) or
when someone says to try again by ending the stalled session: `yardr bead
release <bead>`, which also sends that bead to the group again. An agent
that someone got going in its pane lifts the hold as well. `--set
stall=off` turns the check off for a group. Only bead sessions are judged,
not a resident's.

## Runners

| runner | starts | config |
|---|---|---|
| `manual` | nothing; the bead waits on the group | — |
| `exec` | `sh -c cmd` in the rig, with `YARDR_BEAD`, `YARDR_SESSION`, `YARDR_STAGE`, `YARDR_GROUP`, `YARDR_ACTOR` (the group); for a bead with a rig also `YARDR_RIG`, `YARDR_RIG_PATH`, `YARDR_BASE` (what the bead lands on: the rig's base, or its epic's branch, see [pipeline.md](pipeline.md#epics)), `YARDR_BRANCH` (`yard/<bead>`), `YARDR_WORKTREE` (where herdr groups check that branch out); and `YARDR_BIN`, `YARDR_HOME` for the script's own `yardr` commands; `YARDR_RIG_KIND` is `git`, or `dir` for a dir rig, whose beads get no base, branch or worktree. A refinery is one ([pipeline.md](pipeline.md#4-approved-the-refinery-lands-it-on-main)), and so is an integrator such as `yardr-integrators`, which integrates an epic's branch once its children have landed ([pipeline.md](pipeline.md#the-epics-own-flow)) | `cmd`, `dir`, `log`, `worktrees` |
| `herdr` | a herdr worktree workspace on branch `yard/<bead>` (from the bead's base) at `~/.yardr/worktrees/<rig>/<bead>` (reopened for later stages, one tab per stage), an agent in it, in a tab of its own with `YARDR_ACTOR` (the agent), `YARDR_BEAD`, `YARDR_SESSION`, `YARDR_STAGE`, `YARDR_GROUP`, `YARDR_BASE` (what it branches from: the rig's base, or its epic's branch), `YARDR_HOME` and `YARDR_BIN` in its environment, prompted with a brief in the worktree's git dir (`git rev-parse --git-path yardr`); a dir rig's bead works in the directory itself (see [Dir rigs](#dir-rigs)) | `kind`, `prompt`, `args`, `model`, `keep`, `worktrees`, `idle`, `max_age`, `grace`, `stall`, `label`, `env_file`, `trust` |

### Dir rigs

A dir rig ([configuration.md](configuration.md#rig-kinds-git-and-dir)) is a plain directory,
with no git, base or branches, so its beads are worked in the directory
itself. A herdr group opens a workspace there for the bead (`herdr
workspace create --cwd <dir>`, labelled as any bead's; later stages reuse
it while it is open), a tab per stage with `YARDR_RIG` and `YARDR_RIG_PATH`
in place of `YARDR_BASE`, and writes the brief to `<dir>/.yardr/briefs/`.
The brief says to work in the directory directly, commit nothing, touch
only what the bead names and say in the note what was produced, by path.
Once the bead closes its briefs are removed and its workspace closed; the
directory is left as it is, so there is nothing to `keep`. An exec group
runs in the directory with `YARDR_RIG_KIND=dir` and no `YARDR_BASE`,
`YARDR_BRANCH` or `YARDR_WORKTREE` (not even ones the daemon inherited).
Epics there only group: no epic branch is made and children work in the
same directory. The integrator, refinery and pull request scripts in
`contrib/refinery` work on branches, so a dir rig's flow must not route to
them (routes are per rig): its epic flow sends `open` to reviewers instead,
and each script refuses a dir rig's bead with exit 2 as a guard. Several
beads may work the same directory at once; the group's `limit` is the only
control, so set it to 1 where their files could collide.

A session ends when the bead is advanced, closed or released. If the runner
reports the work gone (process exited, herdr agent gone) the session is marked
`died` and the bead is routed again after a backoff; after 3 failures in one
stage the bead is held (`yardr bead unhold` grants fresh attempts).
A runner that cannot start anything (herdr missing or down) is an outage, not
a failure of the bead: the group backs off and logs `runtime_down` once.
Groups can also limit how long a session sits idle or runs (`idle`,
`max_age`, `grace`; [configuration.md](configuration.md#session-limits-and-outages)).

### The prompt

A herdr group's `prompt` is the role its agents start from, at the head of
their brief. `--set prompt=roles/builder.md` reads the rig's own
`.yardr/roles/builder.md` as it is on the rig's base branch (a bead cannot
change its own reviewer's prompt), else the generic
`~/.yardr/roles/builder.md` that `yardr init` installed; an absolute path is
read as is. The order and the older rig-root `roles/` are in
[configuration.md](configuration.md#role-prompts).

### Outbound credentials

A herdr group whose agents need a credential to act outside the yard (a
review bot's GitHub token, say) names an env file with `--set
env_file=~/.yardr/groups/<group>.env`. The rules are the residents'
([residents.md](residents.md#outbound-credentials)): `KEY=VALUE` lines, `#`
comments, no expansion, mode `0600` or stricter, and the store never holds
the values (BDP serves it, backups copy it, every builder reads it). Each
entry joins the agent's tab environment next to `YARDR_ACTOR` and the rest;
a key yardr sets itself is ignored.

```sh
yardr group add yardr-reviewers --runner herdr --set kind=codex … \
  --set env_file=~/.yardr/groups/reviewers.env
```

A file that is missing, not a regular file or open to others stops the
group, not the bead: nothing is created, the session is aborted without
counting against the bead, and the group backs off and logs `runtime_down`
once with the reason, as for a runtime outage, until the file is fixed.

### Codex

Codex (seen with codex-cli 0.160.0) runs its shell commands in a background
server shared by all of the user's Codex sessions (`codex app-server
--managed-daemon`), which outlives the session that started it and keeps
that session's environment. Every later session's `yardr` commands then
carry the first one's `YARDR_ACTOR`, `YARDR_SESSION` and `YARDR_BEAD`:
notes are attributed to another bead's agent, and `yardr bead done` is
refused as stale, leaving the bead with an idle session. Two things hold
against that:

- **A `kind=codex` agent is started with `--no-daemon`**, bead agent and
  resident alike, whatever the group's `args` say. Its commands then run
  under its own pane, with the tab's environment, and the store recognises
  them by the process tree as it does any agent's
  ([pipeline.md](pipeline.md#2-new-a-builder-implements-it)). The installed
  Codex must know the flag (`codex --help`).
- **The worktree names its session.** A herdr group writes who it started
  in a bead's worktree to `session.json` next to the brief (bead, session,
  actor, stage, group; replaced by each stage). A `yardr` command whose
  `YARDR_BEAD` names another bead than the worktree it runs in acts as the
  worktree's session, not the environment's, and says so on stderr: its
  note is that agent's and its move is checked as that session. So an
  agent whose shell is shared all the same (a Codex started by hand, or
  another kind that does the same) is not taken for another, as long as it
  runs `yardr` from its worktree. Only another bead's environment gives
  way: one naming the worktree's own bead is an earlier stage's agent left
  open and stays refused as stale, and one naming no bead (a person, a
  resident) acts as itself. A pane the store knows still wins: a call from
  another session's own pane is refused, wherever it stands.

A dir rig's beads share one directory, which names no session: there only
`--no-daemon` holds. Sessions started before this (still on the shared
server) keep the wrong environment until they end; spell the values out
for them, `YARDR_ACTOR=… YARDR_SESSION=… YARDR_BEAD=… yardr bead done …`.

### herdr setup

- **Folder trust.** Claude Code asks whether it may trust a folder the first
  time it is started in a repository or a plain directory, and has no flag or
  setting that skips the question in an interactive session. yardr answers it:
  when the agent stops at the dialog, yardr selects "Yes, I trust this folder"
  in its pane and sends the first prompt as soon as the agent is ready. Making
  a folder a rig (or a resident's directory) is what says you trust it. The
  session's `started` event carries `trust_accepted: true` when this
  happened. Claude Code records the answer against the repository, so it
  comes up once per rig, not per worktree. Set `--set trust=false` on a group
  (or resident) to answer it yourself: the session then waits as
  `session_blocked` and gets its first prompt once you have. yardr answers
  only this dialog, and only when its cursor is seen on the accepting answer;
  anything else an agent asks is left to you.
- **Decide what agents may run unattended.** Anything outside `args`'
  permissions blocks the agent on a prompt. Pass what the role needs, e.g.
  `--set 'args=["--permission-mode","acceptEdits","--allowedTools","Bash(yardr:*),Bash(git:*)"]'`;
  compound shell commands still prompt unless allowed or auto mode is on.
- **Blocked agents are reported.** When herdr sees an agent waiting on a
  prompt, yardr writes a `session_blocked` event (visible in `yardr watch`);
  answer it in herdr and the session carries on.
- **Worktrees are cleaned up when the bead closes**, not when a stage ends,
  so later stages reuse them. A worktree with uncommitted or untracked changes
  is kept (a `workspace_kept` event says why) and `yard/<bead>` branches are
  never deleted. Set `--set cleanup=false` on a group to keep its worktrees.
  Reopening a bead checks its branch out again.
- **Labels.** herdr nests each workspace under its rig's node, so labels
  leave the rig out: a bead's workspace is `<group> [<epic>] <bead>` (the
  epic only for a child of one) and its stage's tab `<stage> [<epic>]
  <bead>`, with `yardr-` trimmed from `yardr-sv6b`: `builders [sv6b] p4cv`
  and `new [sv6b] p4cv`, or `builders cbdc` and `new cbdc`. An id that does
  not start with `<rig>-` stays whole, and ids everywhere else in yardr stay
  full. The group appears as its `label` config when set (`--set
  label=codex` on `yardr-reviewers` gives `codex [sv6b] p4cv`), else as
  its name without the `<rig>-` prefix. Residents' workspaces are labelled
  with the resident's name.
- The daemon subscribes to herdr's event stream, so an agent that exits is
  noticed within a second; the reconcile timer remains the fallback.
