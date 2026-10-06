summary: A new car waits here until the yardmaster has shaped it and made it a child of its train.

A car waits here, where `yardr bead create -t car` puts it, until the
yardmaster has shaped it and made it a child of its train
(`yardr dep add <train> <car> -k parent`). No agent works this stage.

A car with no train does not leave: yardr refuses the move to `new`, because
a car lands under the light gate with no review of its own, which is only
safe on a train's branch. Work that stands alone is a task, on the normal
path (`yardr bead update <id> --type task`).

The way out is `yardr bead advance <id> --to new`, to the builders.
