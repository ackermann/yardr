summary: A new part waits here until the yardmaster has shaped it and made it a child of its epic.

A part waits here, where `yardr bead create -t part` puts it, until the
yardmaster has shaped it and made it a child of its epic
(`yardr dep add <epic> <part> -k parent`). No agent works this stage.

A part with no epic does not leave: yardr refuses the move to `new`, because
a part lands under the light gate with no review of its own, which is only
safe on an epic's branch. Work that stands alone is a task, on the normal
path (`yardr bead update <id> --type task`).

The way out is `yardr bead advance <id> --to new`, to the builders.
