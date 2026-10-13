summary: the decided design document lands on the depot's base, as any change does

The direction is decided. The bead's branch holds one document under
`docs/design/` and no code; it lands on the base as any change does, after
the depot's test command.

If the decision's answers are in a note and not in the document, that is as
intended: the document is the proposal as it was decided on, and the note
says what was decided. `failed` sends it back to `draft` when it does not
land (a conflict, a test that fails); `question` to `decide` when something
in it should not be on the base.
