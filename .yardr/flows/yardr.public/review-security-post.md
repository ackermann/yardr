summary: The yard posts the security reviewer's stored review on the pull request as the GitHub App, and sends the bead on the way the review says.

The review the security reviewer stored on the bead is posted here, in public,
by `contrib/assembly/post-review`, the `cmd` of an exec group, exactly as
`review-post` posts the first reviewer's (its stage file has the details: what
is posted and from what, whose pull request, the head that was read, once, and
what is held). No agent and no person works this stage, and the reviewer holds
no key.

The stage takes two outcomes of the stored review, and reports the one it
names:

- `done`: posted as an approval, and the script gives the bead `merge-ok`, the
  permission the App merges on: the security review of the head is the yes.
  The bead goes to `ciwait`.
- `changes`: posted as a request for changes. The bead goes back to `new`; its
  next push updates the same pull request, and it goes through `review` again.
  The flow limits the rounds back to `new` (`max 3 then decide`).
- `failed`: the review could not be posted as stored (none, a field wrong, an
  outcome this stage does not take such as `done-safe`, no head recorded, a
  pull request that moved since the head was read, a comment GitHub refuses).
  The bead goes back to `review-security`, once, with the reason in the note;
  a second failed post goes to `decide`.

A run that is no verdict on the review holds the bead here with a note and
reports nothing; act on what the note says, then `yardr bead unhold <id>`.
