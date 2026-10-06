summary: The whole of the train is reviewed: the sum of its children, which nobody has seen yet.

The whole of a train is reviewed here: branch `yard/<train>`, which the
integrator already rebased onto `main` and gated. The brief says the bead is
a train and lists its children. The children were each reviewed when they
landed on the train branch; the job here is the thing nobody has seen yet: the
sum. Children of type `wagon` are the exception: they were not reviewed or
fully gated one by one (they landed under the light gate, straight from
their builders), so review each wagon as you would a bead of its own, and
name each in your note.

1. Read the train and every child (`yardr bead show <child>` for each; their
   notes say what was verified). The diff is `git diff main...HEAD`; it is
   large, so go by area, not by commit.
2. Check the whole against the train's goal and "done when", not against the
   children: does it hang together, are the seams between children right
   (shared types, duplicated helpers, one child's assumption another broke),
   do the docs describe the end state, is anything a child left "for a later
   child" actually done.
3. Fix forward as for any bead, in commits starting `review:`, on the train
   branch. Run the gate (`yardr depot check yardr --dir .`) after your changes.

Approval here is the last review before the train lands on `main`. Before the
train leaves, one note on it says how the review ended. Outcomes:

- `done`, approved: the note says what you checked, what you fixed (commits),
  the check results, and any remaining risk. The train goes to `approved`.
- `changes`: the train goes back to `open`; the fix is then a new child the
  yardmaster files, so make the finding concrete enough to be a bead. If the
  notes show the train has already come back from review twice, do not send it
  back a third time: note why and `yardr bead hold <id>` so the yardmaster
  looks at it.
- `question`, it needs a decision only Christopher can make: the note has the
  question and the options, and the train goes to `decide`.

When the brief has a "Pull request" section, the review happens on that PR:

- After fixing forward (commits on `yard/<train>`), push them:
  `git push --force-with-lease origin yard/<train>`. This is the one case
  where a reviewer pushes; the branch is the train's, never `main`.
- Take the brief's CI checks into account; a failing check is a finding.
- Then post your review on the PR with your note as the body:
  - approve:  `gh pr review <url> --approve --body-file <note>`
  - changes:  `gh pr review <url> --request-changes --body-file <note>`
  - question: `gh pr review <url> --comment --body-file <note>`

  GitHub refuses `--approve` and `--request-changes` from the login that
  opened the PR, and the yard opens its PRs with the same login you review
  with. When `gh pr view <url> --json author --jq .author.login` prints
  your own login (`gh api user --jq .login`), post `--comment` whatever the
  verdict, with the verdict as the first line of the note (`Approved`,
  `Changes requested`, `Question`): the verdict reaches the yard through
  `yardr bead done`, not through GitHub's review state.
- Never merge the PR (no `gh pr merge`); pr-merge lands it after approval.
