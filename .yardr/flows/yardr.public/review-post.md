summary: The yard posts the reviewer's stored review on the pull request as the GitHub App, and sends the bead on the way the review says.

The review the reviewer stored on the bead is posted here, in public. No
agent and no person works this stage: `contrib/assembly/post-review`, the
`cmd` of an exec group, takes the bead, posts the stored review on the pull
request as the yard's GitHub App, and sends the bead on (docs/line.md, "The
public line"). It is the only place a review is posted from, and the only
holder of the App's key besides the scripts that open and merge: a reviewer
holds none, and runs nothing that posts, so a reviewer misled by what it
reads cannot act as the App.

- **What is posted.** One GitHub review on the pull request the bead tracks,
  made from the stored review (the bead's metadata `review`: `outcome`,
  `body`, `comments`) and from nothing else of the bead: its body and notes
  are not read. The verdict (`Approved`, `Changes needed`) is written as the
  first line, and the comments go inline on the right side of the diff. The
  App opened the pull request and GitHub does not let that account approve it
  or ask changes of it, so the review goes up as a comment review.
- **Whose pull request.** The one the bead tracks, which must also be the
  bead's `own_pr` and be the pull request of the bead's own branch,
  `yard/<bead>`, in the repository it is addressed in. A bead whose metadata
  names any other pull request is held, and nothing is posted.
- **On the head that was read.** The review is of the head the reviewer
  recorded (`reviewed_head`), and the script posts it only when GitHub says
  the pull request has that head now, anchored to it.
- **Once.** When the review is posted the stored review is emptied, so that no
  later round finds it, and the bead is reported.
- **The yes.** For `done-safe`, the approval that needs only the yard's tests,
  the script gives the bead `merge-ok`, the permission the App merges on. The
  reviewer does not.

Outcomes, the review's own, and `failed`:

- `done`: posted. The bead goes to `review-security`.
- `done-safe`: posted, and the bead has `merge-ok`. The bead goes to `ciwait`.
  The yard holds the paths on `.yardr/security_paths` to the security review:
  a `done-safe` the yard refuses holds the bead here, removes `merge-ok`, and
  says to send the bead to `review-security` by hand. The review is already on
  the pull request, so do not unhold it.
- `changes`: posted as a request for changes. The bead goes back to `new`; its
  next push updates the same pull request. The flow limits the rounds back to
  `new` (`max 3 then decide`).
- `failed`: the review could not be posted as the reviewer stored it, and the
  note says why: none stored, a field wrong, an outcome this stage does not
  take, no head recorded, a pull request that has moved since the head was
  read, or a review GitHub refuses as written (a comment on a line that is not
  in the diff). The bead goes back to `review`, once; a second failed post goes
  to `decide`, with the reason in the note.

A run that is no verdict on the review holds the bead here with a note and
reports nothing: a pull request that is not the bead's own or is not open, no
token (the group's environment has no `GITHUB_APP_ID` and
`GITHUB_APP_PRIVATE_KEY_FILE`), a mint that fails, or GitHub refusing the App.
That is the yard's to put right: act on what the note says, then `yardr bead
unhold <id>` runs the script again. A hold after the review was posted says so,
and the bead is then moved on by hand, since posting it again would put it on
the pull request twice.
