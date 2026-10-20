summary: The whole train is reviewed once more, for security alone: every wagon, and what the sum opens.

The whole of a train was reviewed against its goal in `review`, and is
reviewed here a second time, on the branch `yard/<train>`, for security
alone: what the train opens, to whom. Whether it does what it says, hangs
together and is tested was `review`'s question and is not asked again: this
stage must not become a second general review.

The train's body names its wagons and the brief lists its children. A wagon
landed on the train branch with no review of its own and only the plain run
of `.yardr/test`, so this is the one security review it gets, and the one
review of what the wagons open together: a wagon that adds a caller and
another that adds a way to reach it look harmless one by one. The diff is
`git diff main...HEAD`; go by area, not by commit (`git log main..HEAD`
lists the commits, and each child's notes, `yardr bead show <child>`, say
which are its), but name each wagon in your note, so that none is left
unread.

Check the diff against this list:

- what the change lets a caller do that it could not before, and who the
  caller may be: an agent in a pane, a user, a device on the listener, a
  linked yard, a webhook;
- checks that are the operator's or by pane (`maySource`, `operatorOnly` and
  their like): kept, or weakened;
- text from outside the yard (another yard, a source, a hook, a bead's
  body): treated as data, never run, and never trusted for who someone is;
- secrets and tokens: in no note, event, log, error text or argument
  (docs/design/2026-10-12-yardr-0qs5-a-secret-from-a-person.md, section 3.5,
  has the list of where a value must not be);
- files: written with the modes the yard uses (0600), at paths not taken
  from a caller;
- anything that listens, and on what address;
- new dependencies (`go.mod`, a tool a script calls);
- what judges a bead and what lands it: `.yardr/` (the flows, the roles, the
  test gate) and `contrib/assembly`.

A train that touches none of this is passed quickly: the note is one line
saying so, naming the wagons, and the outcome is `done`.

A small finding is fixed on the train branch (commits subject
"<bead id> review: <what>"), not sent back, with a regression test. Run
`YARDR_LANDING=1 sh .yardr/test` after your changes: it runs the selection
plus the six black-box suites when compiled content changed, and the plain
selection alone when nothing compiled changed (docs/line.md, "The landing
run, and the whole suite on a quiet machine"). One note on the train says
how the review ended. Outcomes:

- `done`, passed: the note says what the train opens and to whom, or that it
  touches nothing on the list, names each wagon, says what you fixed
  (commits), the check results, and any risk that remains. The train goes to
  `approved`.
- `changes`: the train opens something it must not and closing it is more
  than a small fix. The train goes back to `open`; the fix is then a new
  child the yardmaster files, so make the finding concrete enough to be a
  bead: the caller, what they can do, and the line that lets them. If the
  notes show the train has already come back from review twice, do not send
  it back a third time: note why and `yardr bead hold <id>` so the
  yardmaster looks at it.
- `question`, what it opens is a choice only Christopher can make (a new
  listener, a check loosened on purpose, a new caller of something that was
  the operator's): the note has the question and the options, and the train
  goes to `decide`.

When the brief has a "Pull request" section, the review happens on that PR,
as in `review`:

- After fixing forward (commits on `yard/<train>`), push them:
  `git push --force-with-lease origin yard/<train>`. The branch is the
  train's, never `main`.
- Take the brief's CI checks into account; a failing check is a finding.
- Then post your review on the PR with your note as the body
  (`gh pr review <url> --approve`, `--request-changes` or `--comment`, with
  `--body-file <note>`). GitHub refuses `--approve` and `--request-changes`
  from the login that opened the PR, and the yard opens its PRs with the
  login you review with: then post `--comment` whatever the verdict, with the
  verdict as the first line of the note (`Approved`, `Changes requested`,
  `Question`). The verdict reaches the yard through `yardr bead done`, not
  through GitHub's review state.
- Never merge the PR (no `gh pr merge`); pr-merge lands it after approval.
