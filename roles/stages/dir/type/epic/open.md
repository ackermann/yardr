The epic waits here while any child is open: each child blocks it. Once the
last child closes the epic is ready, and whoever the stage is routed to (the
rig's reviewers, by a route for `open --type epic`) takes it up.

Nothing is integrated in a directory rig: the children's outputs are already
in the directory. Before the epic leaves, every child is closed and the
rig's gate passes if it has one (`yardr rig check <rig>`).

Outcomes:

- `done`: the above holds. The epic goes to review, where what the children
  produced is looked at as a whole.

The edge back to `backlog` has no outcome: the yardmaster takes the epic back
with `yardr bead advance <epic> --to backlog` to reshape it or add children.
