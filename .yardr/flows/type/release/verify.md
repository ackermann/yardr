summary: The release group checks the signature against the release key, the archives against the checksums, and each archive's CI attestation.

The release group works this stage (`contrib/release/stage verify`): it
downloads `checksums.txt` and `checksums.txt.minisig` from the release,
checks the signature against the release key, checks the release's
archives against the checksums, and checks each archive's
build-provenance attestation (`gh attestation verify`), requiring the
yardr release workflow and the tag being verified. No agent works this
stage by hand.

Outcomes:

- `done`: the signature is good, the archives match the checksums, and
  every archive's attestation verifies. The bead goes to `publish`.
- `failed`: the signature is missing or bad; the note carries what minisign
  said. The bead goes back to `sign`, where mckean is asked again.
- `question`: the release itself is wrong (the archives do not match the
  checksums, an archive's attestation does not verify) or the check could
  not run at all. The note says which. The bead goes to `decide`, to a
  human: a release whose archives are wrong is remade by hand (the release
  and the tag deleted, the bead back to `build`).
