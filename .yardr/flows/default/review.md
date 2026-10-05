summary: The finished bead is reviewed against its goal; what the reviewer can fix is fixed, not sent back.

The finished bead is reviewed here, on its branch `yard/<bead>`, against the
bead: its goal, its "done when" and the builder's notes. The review is
forward: what the reviewer can fix with confidence is fixed on the branch
(commits starting `review:`), not sent back.

Approval here is the last review before the bead is merged; no human reviews
after it, so be thorough.

Before the bead leaves, the rig's gate has run on the branch as it now is
(`yardr rig check yardr --dir .`), and one note on the bead says how the
review ended. Outcomes:

- `done`, approved: the note says what you checked, what you fixed (commits),
  the check results, and any remaining risk. The refinery lands the bead from
  `approved`.
- `changes`, it needs the builder: the design is wrong, the bead was misread,
  or a large part is missing. The note gives concrete, actionable findings,
  and the bead goes back to `new`. If the notes show the bead has already
  come back from review twice, do not send it back a third time: note why and
  `yardr bead hold <id>` so the mayor looks at it.
- `question`, it needs a decision only Christopher can make: the note has the
  question and the options, and the bead goes to `decide`.

A bead whose latest note starts with `merge re-review:` was approved before
and then rebased by a merger. Only the conflict resolution is reviewed. Sound:
the note has the range-diff summary and the gate result, and the outcome is
`done`. Not sound, and not fixed forward: `changes` with a concrete finding,
or `question` if it needs Christopher's choice.
