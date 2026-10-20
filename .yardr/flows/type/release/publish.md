summary: The release group releases the draft, files the yardr.dev sync bead and advances it to new.

The release group works this stage (`contrib/release/stage publish`): the
release is built, signed and verified, and what is left is the site. It
releases the draft (`gh release edit --draft=false`), files the yardr.dev
sync bead (`scripts/sync-release yardr <version>`, priority 1,
`difficulty:1 size:small`), advances it to `new`, and notes its id on this
bead. No agent works this stage by hand.

Outcomes:

- `done`: the draft is released and the sync bead is filed and on its way.
  This bead goes to `published`; the release is the site's now.
- `failed`: the draft could not be released, or the sync bead could not be
  filed or advanced; the note says what stopped it and the id, where one
  was made. The bead goes to `decide`, to a human.
- `question`: filing it needs a person's decision; to `decide`.
