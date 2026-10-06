summary: A train is shaped here: its goal, its "done when" and its children.

A train is shaped here: its goal and "done when" are written and its children
added (`yardr dep add <train> <child> -k parent`, which also makes each child
block the train). No agent works this stage.

In a directory depot a train only groups its children, which work in the same
directory; there is no train branch. The train leaves for `open` once it has
its children and they cover its goal: `yardr bead advance <train> --to open`.
The stage has no outcomes. The children are shaped and advanced on their
own, in the depot's flow.
