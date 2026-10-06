summary: The finished bead is reviewed in the depot's directory; what the reviewer can fix is fixed in place.

The finished bead is reviewed here, in the depot's directory: the outputs the
builder's note names, against the bead (its goal, its "done when"). There is
no branch and no diff against a base. The review is forward: what the
reviewer can fix with confidence is fixed in place, not sent back.

Nothing merges in a directory depot, so approval here is the end of the bead:
`done` closes it, and nobody looks after you. Be thorough.

Before the bead leaves, the depot's gate has run if it has one
(`yardr depot check <depot>`), and one note on the bead says how the review
ended. Outcomes:

- `done`, approved: the note says what you checked, every file you changed,
  the gate result, and any remaining risk. The bead is closed.
- `changes`, it needs the builder: the bead was misread, or a large part is
  missing. The note gives concrete, actionable findings, and the bead goes
  back to `new`. If the notes show the bead has already come back from review
  twice, do not send it back a third time: note why and `yardr bead hold <id>`
  so the yardmaster looks at it.
- `question`, it needs a decision only {{operator}} can make: the note has
  the question and the options, and the bead goes to `decide`.
