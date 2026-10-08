summary: The train's pull request is open; a person says whether it is merged.

The train's pull request is open against `main`, at the commit the reviewer
approved (`pr_reviewed_sha` in the train's metadata). A person looks at the
pull request and its checks and says whether it is merged. Only the pull
request path comes through here (docs/pipeline.md#through-a-pull-request).

Outcomes:

- `done` means "merge it": the required checks are green on the reviewed
  commit and nothing in the pull request speaks against landing it. The train
  goes to `land`. The daemon reports this itself once every check `main`
  requires is green on the reviewed commit; a base that requires none is
  never green to it, so a person says merge.
- `changes`: something in the pull request must be fixed. Add the fix as a
  child of the train first, then report; the train goes back to `open` and
  waits for the child.
- `question`: it needs a decision; the note has the question and the options,
  and the train goes to `decide`.
- `merged`: no person or session reports this one. It is what GitHub's event
  says when the pull request is merged, through the table on the yard's
  github hook (`yardr hook show gh`), and the train goes to `merged`. The
  same table reports `changes` for changes a person requests on the pull
  request and for a required check that failed, and `question` for a pull
  request closed unmerged.

Never merge the pull request by hand from here: `land` merges the reviewed
commit and no other.
