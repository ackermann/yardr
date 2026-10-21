summary: The landed change is deployed to the site's platform from here; a failure goes to a person.

The bead landed on the base from `approved`; the deploy group now deploys
that commit to the site's Void platform with the pack's script. No agent
works this stage. The bead carries the deploy's note when it leaves.

Outcomes:

- `done`: the site is live at the address the note names
  (`live <url> at <sha> in <n> s`), and the bead goes to `live`, which
  closes it. A note with no address means the deploy went out but the
  script could not read the address back from the deploy's log; the site
  is live all the same.
- `failed`: the site is not live. The code has landed: what can be fixed
  now is the platform, the token, or the site's own code, not the landing.
  The note carries the end of the Void CLI's output, and the bead goes to
  `decide`, to a person.

A failure may be the site's own (its build, its config, a migration) as
well as the platform's or the token's. What repairs it is a fix bead
forward: a migration is undone by a new migration, never by going back, so
the fix lands and deploys as any change does. For a code-only fault the
person may instead roll the live site back first (`void project rollback`,
run by them, from a directory linked to the project), then file the revert
bead; until it lands the deploy group is paused
(`yardr group set <site>-deploy --limit 0`, back to 1 after) so no landing
goes live over the rollback. The pack's setup notes say the same under
"The way back" (`yardr pack show website`).
