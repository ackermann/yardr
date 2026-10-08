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
- `roles/`: the prompts.
- `scripts/`: what a script group runs.
- `flows/default/`: a stage file for every stage a bead is worked in.

Not in a pack: agent products and models, depot paths, people, secrets, source
identities, the hook address. What tests a depot belongs to the depot: its
test command (`yardr depot add --test`), which may name a script in its
repository.

The pack's opinion on testing is in its roles and its script, not in yardr:
builders and reviewers run the depot's test command in their worktree before
they hand on, and end their note with `test: <command> @ <commit>: pass`;
`scripts/merge-local` runs it once more on the rebased commit, with
`YARDR_LANDING=1`, before it moves the base. A red run sends the bead back
with the end of the output, exit 2 ("could not run") holds it, and a depot
with no test command lands untested, with a note saying so. A command that is
the path of a file on the base (`--test .yardr/test`) is run from the base's
copy, so a bead cannot change the script that judges it.

Importing sets the yard to this state: what exists under the same name is
overwritten, nothing is added twice, and what the yard chose locally (agent,
model, arguments, members, env file) is left alone.

One thing it asks for and does not set: that the yardmaster may open threads in
Linear (`open_threads`). The import puts that to the yard's operator, once
the yard has a Linear hook and the yardmaster a credential, and a no leaves the
yardmaster as it is.

One thing it sets only where the yard is ready for it: the stage map for
Linear (`sources.linear`), which moves the issue a task tracks to started,
in review and done as the task enters `new`, `review` and `merged`. The
import puts it on the yard's Linear hook, acting as the crew member that takes
`yardmaster`. It makes no hook and does not let that crew member set a status
(`set_state`): the plan names the command for each, and the next import
sets the map once the hook is there.
