summary: A conflict between the train's branch and the base is resolved.

A conflict between the train's branch and the base is resolved here:
rebasing `yard/<train>` onto the base conflicted. The train was already
integrated, reviewed or approved; the job is only to make it apply on top
of what landed since, keeping the intent of both sides.

The work is on the train's own branch, `yard/<train>`, rebased onto the
base. The conflict is between the train as a whole and what landed on the
base, so read its children's beads for its side. Do not push
`yard/<train>`: the line pushes it, with a lease.

Before the train leaves as `done`, the branch applies on the base again
and the depot's test command passes on it, and one note on the train says
what each side wanted and how both were kept.

Outcomes:

- `done`: the above holds. The train goes back to `open`, where the line
  tests it, pushes it and opens or updates its pull request before it goes
  to review.
- `rework`: both intents cannot be kept (the base removed what the train
  builds on, or the two changes contradict each other), or the conflict is
  too large to judge with confidence. The rebase is aborted and the note
  says what conflicts and why it cannot be merged mechanically. The train
  goes back to `open`; the rework is a new child the yardmaster files, so
  make the note concrete enough to be a bead.
- `question`: the resolution needs a real design choice, which a person
  must make. The rebase is aborted, the note has the question and the
  options, and the train goes to `decide`.

## Ask
kind: choice
question: Is the conflict resolved?
context:
- The train's branch conflicted with the base; the job is only to make it apply again, keeping both sides' intent.
- Done means the branch applies on the base, the depot's test command passes on it, and a note says what each side wanted and how both were kept. The train goes back to open.
options:
done: The train goes back to open, where the line tests it and opens or updates its pull request.
rework: The train goes back to open with your words as the note; the yardmaster files the rework as a new child.
question: The train parks in decide for a longer look, with your words as the question.
