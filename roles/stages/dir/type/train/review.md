summary: The whole of a train is reviewed here: the sum of what its children produced in the rig's directory.

The whole of a train is reviewed here: what its children produced in the
rig's directory, as a whole. Each child was reviewed when it was done; the
job here is the sum.

Read the train and every child (`yardr bead show <child>`; their notes name
the files). Check the whole against the train's goal and "done when": do the
children's outputs fit together, is anything a child left "for later"
actually done. Fix forward by editing in place, and run the rig's gate if it
has one.

Approval here closes the train. Before it leaves, one note on it says how the
review ended. Outcomes:

- `done`, approved: the note says what you checked, every file you changed,
  the gate result, and any remaining risk. The train is closed.
- `changes`: the train goes back to `open`; the fix is then a new child the
  yardmaster files, so make the finding concrete enough to be a bead.
- `question`, it needs a decision only {{operator}} can make: the note has
  the question and the options, and the train goes to `decide`.
