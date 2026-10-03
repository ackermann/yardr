# yardr starter pack

The pack yardr ships: the way of working of the yard that builds yardr
itself. Its roles are copies of `roles/*.md` and its `scripts/merge-local`
of `contrib/refinery/merge-local`; a test fails when they drift. The format
and how a pack maps to a yard are in [docs/packs.md](../../docs/packs.md).

`yardr pack check packs/starter --rig <rig>` shows what importing it would
change, and `yardr pack apply packs/starter --rig <rig>` imports it.

- `pack.yaml`: the flows (stages), the groups that work them, and the residents.
- `roles/`: the prompts.
- `scripts/`: what a script group runs.

Not in a pack: agent products and models, rig paths, people, secrets, source
identities, the hook address. A rig's gate (`.yardr/check`) belongs to its
repository.

Importing sets the yard to this state: what exists under the same name is
overwritten, nothing is added twice, and what the yard chose locally (agent,
model, arguments, members, env file) is left alone.
