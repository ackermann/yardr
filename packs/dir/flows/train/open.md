The train waits here while any child is open: each child blocks it. Once the
last child closes the train is ready, and whoever the stage is routed to (the
depot's reviewers, by a route for `open --type train`) takes it up.

Nothing is integrated in a directory depot: the children's outputs are already
in the directory. Before the train leaves, every child is closed and the
depot's test command passes if it has one (`yardr depot show <depot>` prints
it as `test:`).

Outcomes:

- `done`: the above holds. The train goes to review, where what the children
  produced is looked at as a whole.

The edge back to `backlog` has no outcome: the yardmaster takes the train back
with `yardr bead advance <train> --to backlog` to reshape it or add children.
