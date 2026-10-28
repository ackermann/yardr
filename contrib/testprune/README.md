# contrib/testprune

Finds which of a Go package's tests can go without losing what the suite
checks, and says what each would save. It deletes nothing: the report is
a list of candidates for a person to judge, ranked by cost.

```sh
contrib/testprune/testprune [-o DIR] PACKAGE

contrib/testprune/testprune ./internal/dispatch
contrib/testprune/testprune -o /tmp/prune ./internal/store
```

Run it from the checkout to measure. It builds the package's test binary
once, instrumented for the package itself, runs the whole suite for the
baseline coverage, then runs each top-level test alone with its own
coverage profile and wall time, one at a time in one stream. Subtests
run with their parent and count with it. Progress goes to stderr; the
reports land as `DIR/testprune.md` and `DIR/testprune.json` (the current
directory), the same numbers in both.

## What a candidate is

A test whose covered statements all lie in other tests of the package is
a candidate: the others already run that code. That is not proof the test
is redundant: its assertions may check something the others only run. A
candidate is removed only by a later bead that keeps the guard in
`docs/testing.md` (the package's statement coverage is not lower after
the removal, and the reviewer reads each removed test's assertions
against the tests that stay).

Candidates are greedy: removing one changes the others' unique counts,
so the report drops the most expensive zero-unique test, recomputes,
and repeats, and names that set in drop order. The per-test table's
unique column instead counts against the full set, before any removal,
so a test's column and its membership can differ.

## What the report holds

- Totals: the package's statement coverage with all tests (from the one
  full-suite run), and what it would be with every candidate removed
  (the union of the rest), to show the bound. Where the alone runs'
  union differs from the full run, the report says so: running apart
  covered more or less than running together.
- Candidates: tests with nothing unique, most expensive first, with
  their combined time.
- Near-duplicates: pairs of tests whose covered sets are at least 95%
  the same (Jaccard over blocks), as candidates to merge into one
  table-driven test.
- Every test: outcome, wall seconds, statements covered, and statements
  no other test covers.

A test that fails or skips alone keeps its row and its profile when the
run wrote one; the verdict column marks it, since a failure alone is its
own finding. A run that left no usable profile is kept out of the sets,
so it can neither accuse another test nor be accused, and the report
names it.

`go test ./contrib/testprune/` runs the analysis against made-up
coverage sets: the greedy removal, the pair cutoff, and the profile
reading.
