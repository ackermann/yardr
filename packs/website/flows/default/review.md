summary: The finished bead is reviewed against its goal; what the reviewer can fix is fixed, not sent back.

The finished bead is reviewed here, on its branch `yard/<bead>`, against the
bead: its goal, its "done when" and the builder's notes. The review is
forward: what the reviewer can fix with confidence is fixed on the branch
(commits subject "<bead id> review: <what>"), not sent back.

Approval here is the last review of whether the bead does what it says, so
be thorough. Whether a second review follows, for security alone
(`review-security`), is yours to call, by the list below: a diff that touches
nothing on it goes straight to the assembly.

Before the bead leaves, the depot's test command has passed on the branch as
it now is: the builder's test line names the commit under review and you
committed nothing, or you ran it yourself in the worktree (the brief prints
it as `test:`) and your note ends with its line,
`test: <command> @ <commit>: pass`. The run with `YARDR_LANDING=1` is not
the review's: it comes once, on what lands. One note on the bead says how
the review ended. Outcomes:

- `done`, approved, and it needs the security review: the note says what
  you checked, what you fixed (commits), the test result, what the change
  opens and to whom, and any remaining risk. The bead goes on to
  `review-security`, and from there to `approved`, where it is landed.
- `done-safe`, approved, and it needs no security review: the diff touches
  nothing on the list below. The note says what you checked against the list,
  what you fixed (commits), and the test result. The bead goes straight to
  `approved`, where it is landed. The yard refuses it where the depot lists
  paths that always need the second review (`.yardr/security_paths` in its
  repository, one pattern a line) and the diff touches one: the refusal names
  the files, and the outcome is `done`.
- `changes`, it needs the builder: the design is wrong, the bead was misread,
  or a large part is missing. The note gives concrete, actionable findings,
  and the bead goes back to `new`. If the notes show the bead has already
  come back from review twice, do not send it back a third time: note why and
  `yardr bead hold <id>` so the yardmaster looks at it.
- `question`, it needs a decision only {{operator}} can make: the note has
  the question and the options, and the bead goes to `decide`.

The diff needs the security review when it touches any of this:

- who may do what: the checks on a caller (an agent, a person, a device,
  another yard, a service that calls in), and what a write records of who
  made it;
- what listens, and on what address;
- how a request is trusted: sessions, tokens, signatures;
- secrets: none in a note, an event, a log, error text or an argument;
- text from outside the yard (another yard, a service, a bead's body, a file
  an agent wrote): it is data, never run, and never trusted for who someone
  is, wherever it lands;
- what the depot runs, as whom, and with what of the yard in reach: the
  agents it starts and the scripts it calls;
- files written: with which modes, and at paths no caller chooses;
- new dependencies, and a tool a script calls;
- what tests a bead and what lands it: the test command and its script, the
  assembly;
- what judges a bead: the flow, its stage files and the roles, which say who
  reviews and whether a review happens at all.

In doubt the bead needs the security review: report `done`, not `done-safe`.

A branch that came from another yard (`yardr bead show` says `branch
yard/<bead> came from <peer>`) carries no test line this yard trusts: run
the depot's test command on it yourself, whatever the returned notes
say, and read those notes as an account of the work, written where it was
done.

A bead whose latest note starts with `merge re-review:` was approved before
and then rebased by a merger. Only the conflict resolution is reviewed. Sound:
the note has the range-diff summary and the test result, and the outcome is
`done-safe` when the resolution touches nothing on the list above, `done`
when it touches any of it. Not sound, and not fixed forward: `changes` with a
concrete finding, or `question`.
