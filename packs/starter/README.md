# yardr starter pack

The pack yardr ships: the way of working of the yard that builds yardr
itself, in a git depot. Its roles and stage files are here and nowhere
else in yardr: no code names one, and what a directory depot asks of a role
is the [dir pack](../dir)'s to say. Its `scripts/merge-local` is a copy of
`contrib/assembly/merge-local`; a test fails when the two drift. It is the
default pack of a yard that names none: `yardr depot init
<depot>` sets a git depot up from it, and `yardr init` takes the
yardmaster's role from it. Where they say `{{operator}}` and `{{yardmaster}}`, an
import puts the yard's operator and the crew member that takes `yardmaster`. The
format and how a pack maps to a yard are in [docs/packs.md](../../docs/packs.md).

`yardr pack check starter --depot <depot>` shows what importing it would
change, and `yardr pack apply starter --depot <depot>` imports it. yardr
carries this pack in its binary, so the name is enough; `packs/starter`, with
the separator, is this directory in a checkout.

- `pack.yaml`: the flows (stages), the groups that work them, and the crew.
- `roles/`: the prompts. `merger.md` belongs to no group of the pack: it is
  for a flow with a `merge` stage, and a group is given it with
  `yardr group set <group> prompt=roles/<name>.md` once the file is in the
  yard's `roles/`.
- `scripts/`: what a script group runs.
- `flows/default/`: a stage file for every stage a bead is worked in.

Not in a pack: agent products and models, depot paths, people, secrets, source
identities, the hook address. What tests a depot belongs to the depot: its
test command (`yardr depot add --test`), which may name a script in its
repository.

The pack's opinion on testing is in its roles and its script, not in yardr:
builders and reviewers run the depot's test command in their worktree before
they hand on, and end their note with `test: <command> @ <commit>: pass`;
`scripts/merge-local` brings the base up to the remote it pushes to,
fast-forward only (a base that diverged from the remote's branch sends the
bead back with a note saying so), and runs it once more on the rebased
commit, with `YARDR_LANDING=1`, before it moves the base. A red run sends
the bead back with the end of the output, exit 2 ("could not run") holds
it, and a depot with no test command lands untested, with a note saying so.
A command that is
the path of a file on the base (`--test .yardr/test`) is run from the base's
copy, so a bead cannot change the script that judges it.

`scripts/merge-local` lands the branch's commits as they are, by rebase,
unless `YARDR_LAND=squash` stands before the script in the assembly group's
cmd: it then lands the bead as one Conventional Commit written from it, with
the bead's title as the subject, the type from its `kind:` label and the
trailers the branch's commits carried (docs/line.md, "Landing by squash"). The
roles ask for what that wants: the yardmaster labels a bead's kind, a builder
or a reviewer titles it for what the change does, and a change that breaks a
user carries a `BREAKING CHANGE:` line.

This pack lands by squash: `assembly` has `env: { YARDR_LAND: squash }` in
`pack.yaml`, and the import writes it before the script in the cmd of the
group it creates, so a depot set up from the pack gets `YARDR_LAND=squash
<script>`. A group the yard has is never changed by it: once a group is made
the assignments before its script are the operator's, so an import of this
pack again, or of a version with another `env`, leaves the cmd as it is and
says in a line what it lacks (`yardr pack check`). A depot whose group
predates this opts in by hand (`yardr group show <depot>-assembly` shows the
cmd; `yardr group set <depot>-assembly 'cmd=YARDR_LAND=squash <the rest of the
cmd>'` sets it), and a depot that wants rebase sets the cmd to the script
alone (`YARDR_LAND=rebase` says it in words). The design, contributors and
website packs set nothing, so they land by rebase (docs/packs.md).

Importing sets the yard to this state: what exists under the same name is
overwritten, nothing is added twice, and what the yard chose locally (agent,
model, arguments, members, env file) is left alone.

One thing it asks for and does not set: that the yardmaster may open threads in
Linear (`open_threads`). The import puts that to the yard's operator, once
the yard has a Linear hook and the yardmaster a credential, and a no leaves the
yardmaster as it is.

One thing it sets only where the yard is ready for it: the stage map for
Linear (`sources.linear`), which moves the issue a task tracks to started,
in review and done as the task enters `new`, `review` (or `review-security`)
and `merged`. The
import puts it on the yard's Linear hook, acting as the crew member that takes
`yardmaster`. It makes no hook and does not let that crew member set a status
(`set_state`): the plan names the command for each, and the next import
sets the map once the hook is there.

## The second review, for security alone

`review` asks whether a bead does what it says. `review-security`, worked by
the group `security-reviewers` with `roles/review-security.md`, asks one other
question of the same diff: what does it open, and to whom? The reviewer sends a
bead on with `done`, or past the second review with `done-safe`, by the list in
`flows/default/review.md`. A train goes through both reviews, on the whole of
its diff.

The import asks nothing for `security-reviewers`. It runs the agent, model and
permission arguments `reviewers` has, as the yard has them or as the import
binds them (`binds_as: reviewers` in `pack.yaml`). `--bind
security-reviewers=<kind>[:<model>]` gives it an agent of its own, so that the
second review is not made by the agent that made the first.

A depot can take `done-safe` out of the reviewer's hands for the paths it
names. `.yardr/security_paths`, a file in its repository, lists them one
pattern a line, as globs from the repository's root (`**` crosses directories,
`*` stays in one). The yard reads it at the tip of the base branch, never from
a bead's branch, and refuses a `done-safe` for a bead whose diff against its
base (a child of a train: against the train's branch) touches a file that
matches. The refusal names the files, and the reviewer reports `done`. The
pack ships no list: the paths belong to the depot, and a pack writes nothing
into a repository. Without the file, `done-safe` is taken as the flow says.
To start:

```
# Paths whose change always has the security review: the yard refuses
# done-safe for a diff that touches one. One pattern a line, from the root of
# the repository; ** crosses directories, * stays in one.
#
# internal/auth/**
# go.mod
```

Listing `.yardr/**` too puts a change to the list itself through both reviews.
[docs/flows.md](../../docs/flows.md#done-safe-and-the-security-paths) has the
rules.
