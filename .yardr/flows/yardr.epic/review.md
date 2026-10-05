summary: The whole of the epic is reviewed: the sum of its children, which nobody has seen yet.

The whole of an epic is reviewed here: branch `yard/<epic>`, which the
integrator already rebased onto `main` and gated. The brief says the bead is
an epic and lists its children. The children were each reviewed when they
landed on the epic branch; the job here is the thing nobody has seen yet: the
sum.

1. Read the epic and every child (`yardr bead show <child>` for each; their
   notes say what was verified). The diff is `git diff main...HEAD`; it is
   large, so go by area, not by commit.
2. Check the whole against the epic's goal and "done when", not against the
   children: does it hang together, are the seams between children right
   (shared types, duplicated helpers, one child's assumption another broke),
   do the docs describe the end state, is anything a child left "for a later
   child" actually done.
3. Fix forward as for any bead, in commits starting `review:`, on the epic
   branch. Run the gate (`yardr rig check yardr --dir .`) after your changes.

Approval here is the last review before the epic lands on `main`. Before the
epic leaves, one note on it says how the review ended. Outcomes:

- `done`, approved: the note says what you checked, what you fixed (commits),
  the check results, and any remaining risk. The epic goes to `approved`.
- `changes`: the epic goes back to `open`; the fix is then a new child the
  mayor files, so make the finding concrete enough to be a bead. If the notes
  show the epic has already come back from review twice, do not send it back
  a third time: note why and `yardr bead hold <id>` so the mayor looks at it.
- `question`, it needs a decision only Christopher can make: the note has the
  question and the options, and the epic goes to `decide`.

When the brief has a "Pull request" section, the review happens on that PR:

- After fixing forward (commits on `yard/<epic>`), push them:
  `git push --force-with-lease origin yard/<epic>`. This is the one case
  where a reviewer pushes; the branch is the epic's, never `main`.
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
