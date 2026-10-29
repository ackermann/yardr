summary: A bead waits here for a person's yes before it lands.

A bead here waits for your yes before it lands: it was built, reviewed and
tested, and landing it is now your call. No agent works this stage. Read the
bead and its notes; the diff is on branch `yard/<bead>` when you want to
look at it yourself.

Answer with `yardr review`, which offers the three outcomes, or move the
bead by hand (`yardr bead done <id> --outcome <outcome>`, with a note):

- `done`: land it. The bead goes to `approved`, where the assembly tests it
  once more on what lands and lands it on the base.
- `changes`: it needs the builder. Your note says what is wrong, concretely,
  and the bead goes back to `new`. Where the flow limits the rounds back to
  `new` (`yardr flow show` writes a limit as `max 3 then decide`), a further
  round past the limit goes to `decide` instead, for a longer look.
- `question`: landing it needs more than a yes or a note; the note has the
  question, and the bead parks in `decide` for a longer look.

## Ask
kind: choice
question: Should this bead land?
context:
- The bead was built, reviewed and tested; landing it is now your call.
- The diff is on the branch named above when you want to look at it yourself.
options:
done: The bead goes to approved, where the assembly tests it once more on what lands and lands it on the base.
changes: The bead goes back to new with your words as the note; past the flow's limit on rounds back, it goes to decide instead.
question: The bead parks in decide for a longer look, with your words as the question.
