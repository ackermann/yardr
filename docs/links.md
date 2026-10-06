# Links to outside issues

A bead records the issue outside the yard it belongs to: a Linear issue, a
GitHub issue. A link is to an issue, not to a bead: edges between beads are
`yardr dep` ([pipeline.md](pipeline.md#trains)).

```sh
yardr link add yardr-k3f9 https://linear.app/acme/issue/YRD-12/web-view   # yardr-k3f9 tracks the issue
yardr link add yardr-k3f9 https://github.com/acme/webapp/issues/7 --relates
yardr link add yardr-k3f9 4812            # the issue of hook delivery 4812, as an inbox line numbers it
yardr link list yardr-k3f9                # the bead's links
yardr link list --issue https://linear.app/acme/issue/YRD-12             # the issue's beads, its tracker first
yardr link move https://linear.app/acme/issue/YRD-12 --to yardr-e2ab     # another bead tracks it from now on
yardr link rm yardr-k3f9 https://linear.app/acme/issue/YRD-12
yardr bead create "Web view shows links" --rig yardr --tracks https://linear.app/acme/issue/YRD-12
```

Linking only records, and calls neither Linear nor GitHub. What acts on a
`tracks` link is a hook with a map: when the bead enters a stage, the issue
moves to the matching status ([below](#a-beads-stage-moves-the-issue)).

## Two kinds

| kind | what it means |
|---|---|
| `tracks` | the bead's stage moves the issue, where a hook maps it. The default of `link add`; `--tracks` on `bead create` |
| `relates` | shown with the bead, never moves the issue. `--relates` on both |

Any bead type may track an issue: a task, a train, a child of a train. A
closed bead keeps its links.

## The rules

- **A bead tracks at most one issue per source.** One Linear issue and one
  GitHub issue together is fine. A second `tracks` link to the same source is
  refused, and the message names the issue the bead already tracks.
- **An issue has at most one tracking bead.** A second tracker is refused.
  The message names the bead that tracks the issue and both ways out:
  `yardr link move`, or link with `--relates`. A closed bead still counts.
- **Any number of `relates` links**, on a bead and on an issue.
- **A bead has one link per issue.** Adding the other kind to an issue the
  bead already links replaces the link, and the command says so. Adding the
  same kind again is refused.

`yardr link move <issue> --to <bead>` is for work that grew into a train:
the train tracks the issue in place of the bead that started it. It is one
write, so the issue is never without a tracker and never has two. The bead
that tracked it keeps a `relates` link. It refuses an issue no bead tracks
(use `link add`) and a target that already tracks another issue of that
source.

`bead create --tracks` and `--relates` make the links in the write that
creates the bead: a refused link creates no bead.

## A bead's stage moves the issue

A hook can carry a map from stages to statuses. When a bead enters a stage
the map names, the issue it tracks in the hook's source goes to that status.
No agent takes part: the yard does it, as a crew member the hook names.

```sh
# Once, by the operator: the yardmaster may set a status in Linear, and its
# credential is in its env_file (LINEAR_API_KEY; for GitHub a GitHub App's
# two lines or GITHUB_TOKEN, see crew.md "As a GitHub App").
yardr crew set yardmaster set_state=linear env_file=~/.yardr/crew/yardmaster.env   # the rest of its definition stays
yardr crew identity yardmaster --source linear --id <its Linear user id>

# The hook of the source, with the map and whom it acts as.
yardr hook add linear --source linear --secret-file ~/.yardr/linear.secret --as yardmaster \
    --map task:new=started --map task:review=review --map task:merged=done \
    --map train:open=started --map train:done=done

yardr bead create "Web view shows links" --rig webapp --tracks https://linear.app/acme/issue/YRD-12
yardr bead advance webapp-k3f9 --to new    # YRD-12: Todo -> In Progress
yardr bead done webapp-k3f9                # into review: In Progress -> In Review
#   … approved is not in the map: nothing
#   … merged: In Review -> Done

yardr hook list                 # the MOVES ISSUES column: as yardmaster: task:new=started …
yardr hook show linear          # the definition, and the moves still to make
yardr events --bead webapp-k3f9 # thread_state, thread_state_skipped
```

A pack can carry the map with the flow it speaks about (`sources.<source>`
in `pack.yaml`): `yardr pack apply` then sets it on the yard's hook of that
source, and neither makes the hook nor grants `set_state`
([packs.md](packs.md#how-a-pack-maps-to-the-yard)).

**The map.** `--map <bead type>:<stage>=<state>`, repeatable. The state is
one of `started`, `review`, `done`, the words of `yardr thread state`, and
each source turns it into a status of its own
([crew.md](crew.md#setting-a-threads-status)). A stage or a type
without a line changes nothing. A train is a bead of type `train` with its
own lines: the issue a train tracks follows the train, and its children's
stages move nothing unless they track issues themselves. `hook add` replaces
the definition, so the whole map is given each time. It refuses a map
without `--as`, a word that is no state (`review` on a GitHub hook too: an
issue there is open or closed), a crew member that does not exist, and one with
no identity in the hook's source.

**When.** A bead of that type enters a mapped stage, whatever moved it: an
outcome, `bead advance`, the move into an end stage that closes it, or
`bead create` straight into the stage with `--tracks`. It must have a
`tracks` link to an issue of the hook's source at that moment; a `relates`
link moves nothing, and a link added to a bead that already sits in a stage
counts from its next stage on. A bead sent back into the stage it is in
(`open -> open on failed`) entered nothing. A bead that tracks a Linear and
a GitHub issue moves both, each through its source's hook.

**As the crew member.** The call is made with the credential in the `--as`
crew member's `env_file` and so appears in the source under its name. It is
made only when that crew member has `set_state` for the source, which only the
operator can grant: a map alone moves nothing. Without the grant nothing is
sent, and the `thread_state_skipped` event names the setting. Adding a map
that acts as a crew member is itself only the operator's, or that crew member's
own session's.

**Forward only.** The states have an order: started, review, done. yardr
reads the issue first and leaves it alone when it stands at or past the
status asked. So a bead sent back from review to new does not pull the issue
back, and an issue a person moved ahead by hand stays where they put it. A
status yardr cannot place counts as past everything: cancelled or duplicate
in Linear, closed as not planned on GitHub. An open GitHub issue stands at
`started`, a closed one is never reopened, and a pull request is never
touched.

**After the event, and once.** The move is written with the bead's event, in
the same write, and made by the daemon afterwards, outside it. When the
source fails, it is tried again after 30 s, then 1 min, doubling up to an
hour, 24 times in all; a daemon or server restarted in between carries on
from the store. `yardr hook show <name>` lists the moves still to make,
with the last error. A move is gone with the event that says how it ended,
so one stage change moves an issue at most once. `yardr hook rm` drops what
the hook had still to do, and `hook_removed` says how many.

**Events**, both on the bead, with `hook:<name>` as the actor:

| event | when | data |
|---|---|---|
| `thread_state` | the issue was moved | `hook`, `crew` (acted as), `source`, `url`, `thread`, `title`, `stage` (entered), `state` (the word), `status` (set), `was`, `instead` (`started`, when review had no status of its own), `cause` (the seq of the bead's event) |
| `thread_state_skipped` | it was not | the same without `was` and `instead`, `status` being the one found, and `reason` with `detail` in words: `no_grant` (the crew member lacks `set_state` for the source, or is gone), `already_past`, `failed` (the source failed for good: given up after `attempts` tries, or something no retry changes, such as a pull request) |

**No echo.** The source reports the change by webhook. That delivery does
not reach the crew member acted as: its actor is the crew member's identity in the
source, which is why `hook add --as` insists on one. `thread_state` does not
reach its inbox either. A delivery is a `hook` event, not a bead entering a
stage, so it moves nothing in turn.

`YARDR_LINEAR_API` and `YARDR_GITHUB_API` in the daemon's environment send
these calls elsewhere, as they do for `yardr thread state`.

## One form per issue

`<issue>` is the issue's URL, or the seq of a hook delivery from its thread
(the number an inbox line starts with, as `yardr reply` takes it). The
source's adapter turns either into the issue's one fixed form, which is what
the link stores:

| source | fixed form | left out |
|---|---|---|
| Linear | `https://linear.app/<workspace>/issue/<KEY-n>` | the title slug (it changes with the title), anything after it, query and fragment |
| GitHub | `https://github.com/<owner>/<repo>/issues/<n>` | anything after the number, query and fragment |

The workspace, owner and repo are lowercased and Linear's key uppercased:
both sources match them in any case. Two links are to the same issue exactly
when these forms are equal, and a delivery's thread is matched to a link the
same way, by its thread URL.

Refused: a URL on another host, a URL of the source that is no issue (a
Linear project, a GitHub discussion), and a GitHub pull request, though
GitHub numbers it with the issues. A delivery from a pull request's thread
or from a `token` hook names no issue either.

A link made from a delivery also records the thread's id in the source. A
link made from the URL takes that id from another link to the issue that has
it, and otherwise has none.

## Where links show

- `yardr bead show <id>` lists them with kind, URL and source;
  `--json` has them as `links`, each with `kind`, `source`, `url`, `thread`
  (when known), `created_at` and `created_by`.
- The event log: `linked` and `unlinked` on the bead, with `kind`, `source`,
  `url`, `thread` (when known) and the actor. A replaced link logs both, the
  `unlinked` with `replaced_by` and the `linked` with `replaced`; a move logs
  them on both beads, with `moved_to` and `moved_from`. Both events re-mint
  the bead's revision, as an edge does.
- BDP: every link is a Link to the issue's URL ([bdp.md](bdp.md#links-to-outside-issues)).
