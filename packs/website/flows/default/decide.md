A bead here waits for a person: {{operator}} decides. No agent works this
stage. The bead carries the question and the options, written by whoever sent
it here; if it does not, ask them before deciding.

A bead leaves once the answer is on it, as a note. The stage has no outcomes;
the person answering moves it (`yardr review`, or
`yardr bead advance <id> --to <stage>`):

- to `new`, when a builder can now finish it;
- to `backlog`, when the answer changes what the bead is and the yardmaster must
  reshape it.

A decision that is not about one piece of work is asked with
`yardr bead ask --new "<question>"`, which makes a bead titled with the
question and the ask on it. Without `--depot` that bead is under no flow and
in no stage's line; `--depot <depot> --stage decide` puts it here instead.
Either way whoever asked closes it once the answer is on it, with what they
did with the answer as the reason (`yardr bead close <id> --reason "..."`).
