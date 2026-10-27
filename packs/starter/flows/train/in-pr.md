summary: The train's pull request is open; a person says whether it is merged.

The train's pull request is open against the base, at the commit the
reviewers approved. A person looks at the pull request and its checks and
says whether it is merged.

Outcomes:

- `done` means "merge it": the required checks are green on the reviewed
  commit and nothing in the pull request speaks against landing it. The
  train goes to `land`, which merges it.
- `changes`: something in the pull request must be fixed. The note says
  what, concretely, and the train goes back to `open`.
- `question`: it needs a decision; the note has the question and the
  options, and the train goes to `decide`.
- `merged`: only the hook reports this one, when the pull request is merged (a hand merge included); it is not an option below.

Never merge the pull request by hand from here: `land` merges it.

## Ask
kind: choice
question: Should the train's pull request be merged?
context:
- The train's pull request is open against the base at the reviewed commit.
- Done means merge it: the required checks are green and nothing in the pull request speaks against landing it. The train goes to land, which merges it.
options:
done: The train goes to land, which merges the pull request.
changes: The train goes back to open with your words as the note.
question: The train parks in decide for a longer look, with your words as the question.
