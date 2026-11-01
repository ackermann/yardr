summary: The release group tags the base, waits for CI's draft release, sets its notes and notes the five signing commands.

The release group works this stage
(`contrib/release/stage build`, docs/install.md "Making a release"): it
tags the depot's base commit, pushes the tag, and waits for the draft
release CI's release workflow makes for the tag, bounded. The workflow
tests, builds the four archives and `checksums.txt` reproducibly (with
`install.sh` copied beside them and named in `checksums.txt` too, so
that the hand signing at the sign stage covers it), attests the archives
and drafts the release; the stage sets the draft's notes from the
bead's body, notes the five signing commands on the bead, for the
version, and reports done. No agent works this stage by hand.

Before the bead leaves as `done`:

- the draft release exists, with the four archives, `checksums.txt` and
  `install.sh`, unsigned, the bead's body as its notes;
- a note on the bead has the five signing commands.

Outcomes:

- `done`: both hold. The bead goes to `sign`, where mckean is asked to sign
  the checksums.
- `failed`: the version, the tag, the tree, CI or the draft stood in the
  way; the note says which, and a failed CI run names its URL. The bead
  goes to `decide`, to a human.
- `question`: the bead itself is wrong, as a rule its title names no
  version. Note what is wrong first; the bead goes to `decide`, to a human.
