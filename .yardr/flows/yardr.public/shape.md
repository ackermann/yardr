A shaper reads the bead and the code it names, and shapes it for a builder.

The shaper changes no code and closes nothing: it reads the bead, the code
the bead names and what that code touches, looks for the mechanism that
already carries the work, and rewrites the body in the yard's shape, keeping
the finding's facts.

Before the bead leaves as `done`, its body has each of these, in order, and
both estimate labels:

- Goal: what is true when the bead is done, and the evidence that it is not
  true today;
- Where to look: files, functions, tests and docs by path, each with what
  the builder finds there;
- What to do: the change, with the choices made, so the builder chooses
  nothing;
- Left out, on purpose: what this bead does not do;
- Done when: checkable lines, each one a test the builder runs or a thing a
  reviewer sees;
- `difficulty:<1|2|3>` and `size:<small|medium|large>`, estimated as the
  shaper's role says.

Outcomes:

- `small`: the above holds and the bead is `difficulty:1` and `size:small`.
  The bead goes to `siding`, to wait for company and be built and reviewed
  with it as one.
- `done`: the above holds and the bead is `difficulty:1` but not
  `size:small`. The bead goes to `new`, to a builder as it stands.
- `question`: the bead is `difficulty:2` or `3` and earns a person's read
  first, is a duplicate of another bead, or needs a decision only a person
  can make. The question is asked on the bead first, and the bead goes to
  `decide`.

The edge back to `backlog` has no outcome: the yardmaster takes a bead back
with `yardr bead advance <id> --to backlog` when it has to be reshaped.
