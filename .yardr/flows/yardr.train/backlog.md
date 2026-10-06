summary: The yardmaster shapes the train: its goal, its "done when" and its children.

A train is shaped here: the yardmaster writes its goal and "done when" and
adds its children (`yardr dep add <train> <child> -k parent`, which also makes
each child block the train). No agent works this stage.

The train leaves for `open` once it has its children and they cover its goal:
`yardr bead advance <train> --to open`. The stage has no outcomes. The
children are shaped and advanced on their own, in the depot's flow.
