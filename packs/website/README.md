# yardr website pack

The pack yardr ships for a website the yard builds and deploys: the
starter's line, from backlog to merged, with a deploy stage after landing.
A landed change is deployed to the site's project on the user's own Void
platform, and the bead closes when the site is live.

It is for a git depot, instead of the [starter](../starter): its one flow
is the depot's, and it has no train flow — a train follows the depot's flow
and deploys as it lands. `yardr pack check website --depot <depot>` shows
what importing it would change, and `yardr pack apply website --depot
<depot>` imports it. yardr carries this pack in its binary, so the name is
enough; `packs/website`, with the separator, is this directory in a
checkout. The format and how a pack maps to a yard are in
[docs/packs.md](../../docs/packs.md).

- `pack.yaml`: the flow, the groups that work it, and the crew.
- `roles/`, `flows/default/` except `merged.md`, and `scripts/merge-local`
  are the starter's, copied: a pack names a group together with the role or
  the script it runs, and these are the starter's groups. A test fails when
  a copy drifts.
- `flows/default/merged.md`: the deploy stage, worked by the deploy group.
- `scripts/deploy-void` is a copy of `contrib/deploy/deploy-void`; a test
  fails when the two drift.
- `SETUP.md`: the setup notes the import prints (`setup:` in `pack.yaml`),
  the checklist for the person whose website it is: the platform, the
  token, the first deploy, the way back.

## The flow

```
backlog -> new | decide
new -> review on done | decide on question | backlog
review -> review-security on done | clear on done-safe | new on changes max 3 then decide | decide on question
review-security -> clear on done | new on changes max 3 then decide | decide on question
clear -> approved on done | new on changes max 3 then decide | decide on question
approved -> merged on done | new on failed max 3 then decide | decide on question
merged -> live on done | decide on failed
decide -> backlog | new | merged
live terminal
```

| stage | worked by | what happens |
|---|---|---|
| `backlog` | `backlog`, people | the bead waits until it is specified well enough to build from |
| `new` | `builders` | an agent implements the bead on its branch |
| `review` | `reviewers` | another agent reviews it against the bead, fixing forward |
| `review-security` | `security-reviewers` | a third agent reviews the diff for security alone |
| `clear` | `decisions`, people | a person says yes before the bead lands |
| `approved` | `assembly` | the branch lands on the depot's base, after the depot's test command |
| `merged` | `deploy` | the landed commit is deployed to the site's Void platform |
| `decide` | `decisions`, people | a person answers the question the bead carries |
| `live` | — | the site is live at the address the bead's note names |

`decide` may send a bead back to `merged` once a person has fixed what a
deploy failed on. What an outside source hears is the starter's: the issue
a task tracks moves to started, in review and done as the task enters
`new`, `review` (or `review-security`) and `merged`, and stays done at
`live`. There is no `github` says: the starter's lines report a train's
pull request, and this pack has no train flow.

## Limits on rounds

The four ways back to `new` (`changes` from `review`, from
`review-security` and from `clear`, `failed` from `approved`) are limited as
the starter's are: 3 rounds, counted together, and the fourth round goes to
`decide`, where a person reads the note that asked for it. The review cycle is
the starter's, copied, so its reasons are too; the
[starter's README](../starter/README.md#limits-on-rounds) gives them. The
deploy stage has no limit: a deploy that fails goes to `decide` at once.

`yardr flow show` prints a limit as `max 3 then decide`. A yard that wants
other numbers changes them in its copy of the pack, or in the flow it keeps
(`yardr flow set`). Importing the pack again gives a yard these limits, and
`yardr pack check` shows them as a change to the flow first.
[docs/flows.md](../../docs/flows.md#a-limit-on-rounds) says how rounds are
counted and what the move to `then` writes to the log.

## The deploy group

The import makes `<depot>-deploy`, an exec group at limit 1 whose `cmd` is
the copied script, and routes `merged` to it. The project and the token
are the yard's, set after the import:

```sh
yardr group set <site>-deploy cmd="VOID_PROJECT=<site> $HOME/.yardr/packs/website/scripts/deploy-void"
yardr group set <site>-deploy env_file=$HOME/.yardr/deploy/<site>.env
```

The assignments before the script and the `env_file` are the yard's, so a
later `pack apply` keeps them: the pack owns only the script word. One
deploy runs at a time, and what is deployed is the base as it stands, not
the bead's own commit, so queued landings go out together.

## The token

The yard holds one secret for this: the platform's project deploy token,
with the platform's API URL beside it, in a 0600 env file the operator
writes. Every agent the yard runs, and the site's own build, can obtain
it, since they run as the yard's OS user. That is accepted, and this is
the statement of it: the token deploys this one project, lasts at most 90
days, and is revoked in one command; whoever can reach it can already get
code onto the base through a landing, which the same script then deploys.
`SETUP.md` says the same beside its token step. The yard holds no
Cloudflare token and no human Void login.

## The way back

A bad landing is undone in two steps, in this order, and neither is the
other:

1. **Stop the harm now: `void project rollback <deployId>`, by the
   person.** From any directory linked to the project, with `void project
   status` to pick the deployment. The yard does not run it. Rollback puts
   the earlier deployment's code back in front of traffic, and may or may
   not put back its schedules, queues, workflows and routes, so those are
   checked afterwards, and **rollback does not reverse database migrations**:
   the older code runs against the newer schema.
2. **Make it stick: revert on the base.** The next landing deploys the base
   again and undoes the rollback, so the revert is filed as a bead, lands,
   and its deploy makes the base and the live site agree; until then the
   deploy group is paused (`--limit 0`, back to 1 after).

`SETUP.md` carries these steps whole, with the commands. A deploy that
fails after landing for the site's own reasons (its build, its config, a
migration) goes to `decide` with the end of the CLI's output; the repair
is a fix bead forward, or the person's rollback for a code-only fault.

## What the import asks

As the starter's: the agents of the groups it makes (`--bind
builders=<kind>`, `--bind reviewers=<kind>`), the members of `backlog` and
`decisions` (`--members`), which crew member takes `yardmaster` (`--crew`),
and whether the yardmaster may open threads in Linear. The deploy group
needs nothing answered: it runs the pack's script.
