# yardr setup

`yardr setup` takes a yard from its onboarding to a first bead in work, as a
short run of forms in the terminal. It is the same setup the yardmaster does
with you in conversation, and you can change from one to the other after any
step.

```sh
yardr setup
yardr setup --json | jq -r '.next.next'   # the command for the next step, no form
```

The onboarding ends in one of the two: once the yardmaster is up, its last
question is "Talk to the yardmaster" or "Keep going here". "Talk to the
yardmaster" opens [`yardr chat`](chat.md#from-the-onboarding) on the
yardmaster in the same terminal, and "Keep going here" is `yardr setup` in the
same run. Both act as the operator the onboarding made, since its "Your name"
takes only who the terminal's commands act as: your login name, or
`$YARDR_ACTOR` ([users.md](users.md#who-is-acting)).

The yardmaster's first job is the setup, as a conversation with you: it asks for
the repository and its base branch, registers the depot, sets it up from the
yard's default pack with `yardr depot init` (the flow, the groups that build,
review and land, their roles and their routes, all kept in the yard, and
nothing in the repository), checks with you what tests the project, and
offers a first bead to try the line. It asks before anything
outward, such as a commit.

What the onboarding itself asks, how it starts the yard and what it does
when the yardmaster does not come up is in
[configuration.md](configuration.md#creating-a-yard-the-onboarding-and-yardr-init).
A harness run for the first time on a machine may show a screen of its own
first (a welcome, a login): the wait for the yardmaster says so once it sees
that, with the pane's last lines and how to reach it, and goes on waiting
for you to answer it in the pane.

## One list of steps

The steps are the ones [`yardr doctor`](doctor.md#setup) prints, read from
the store by one function. `yardr setup` prints them, each `done` or `todo`,
and takes the first one not done. It keeps no state of its own, so it goes on
where the yard stands, whoever did the steps before: these forms, the yardmaster
in conversation with you, or you on the command line. Run it again at any time.

The yardmaster's first run reads the same list through `yardr doctor`. There
is no second list to keep in step with it.

## The pages

Each page asks what its command needs, shows the command line as you would
type it (after `$`, as `yardr --home <home> …` for a home other than the
default) and runs that command.

On a yard whose `operator_style` is `guide`
([configuration.md](configuration.md#how-agents-work-with-you)) the pages
print no command lines: the questions and what each command prints say what
happens. The commands run are the same. With any other style, and with none,
the pages show them.

| step | the page asks | and runs |
|---|---|---|
| a depot | the repository's path (it starts at the directory you are in, when that is a checkout's top), the depot's name (the directory's, in lowercase), the base branch (the branch checked out) | `yardr depot add <depot> <path> --base <branch>`. A directory that is no git checkout is asked for no base and becomes a dir depot |
| the depot's `.yardr` files on its base; a group that builds; a group that reviews; a group or person that lands approved beads; routes for new, review and approved | what the import asks (below) | `yardr depot init <depot>`, which takes the yard's default pack |
| a first bead | its title and its goal | `yardr bead create "<title>" --depot <depot> -b "<goal>"`, then `yardr bead advance <id> --to new` |

The last step, a first merged bead, has no page: it is the line's to do.
There the forms end and say where the first bead is and how to watch it:
`yardr session list`, `yardr bead show <id>`, `yardr watch`, and
`yardr web serve` for the yard in a browser ([web.md](web.md); once the web
view is installed as a login service, its address instead). They also say
so when no daemon is running, since nothing is dispatched then.

### One page sets the depot up

Five of the steps are one import, so they have one page between them:
[`yardr depot init <depot>`](packs.md#yardr-depot-init)
with the yard's default pack: the `default_pack` setting, else the pack
yardr carries for the depot's kind, `starter` for a git depot and `dir` for
a dir depot ([packs.md](packs.md#the-default-pack)). It gives the depot its flow and stage files, the
groups that build, review and (in a git depot) land, the two groups of
people the flow names, and a route for each stage. The roles, the flow and
the assembly's `merge-local` are the pack's, installed under
`<home>/packs/<pack>/` and `<home>/flows/`; nothing of them goes into the
repository.

The command prints its plan first: every file, group, flow and route it
would write. Then it asks what [`yardr pack apply`](packs.md#yardr-pack-apply)
asks:

- for each group of agents, the agent, the model and the permissions (it
  starts at the yard's `default_kind`, with the flags that keep it from
  stopping to ask);
- for each group of people, who is in it (empty for anyone);
- which crew member takes the pack's yardmaster, when the yard has more than
  one;
- `Apply pack <pack> to depot <depot>?`. Nothing is written before this yes.

A yard where part of this was done by hand gets the same page for the rest,
under the title of the first step not done. The plan says what the import
would change there: a group that exists under the pack's name is changed,
and a role the depot has under `.yardr/roles/` is named as shadowed
([packs.md](packs.md#how-a-pack-maps-to-the-yard)). A step counts as done by its
route alone, so a group that was added and never routed is not a group that
builds yet. To finish such a yard by hand instead, add the routes
(`yardr route add --stage <stage> --depot <depot> --group <group>`) and the
steps turn done.

The pack's assembly lands on local branches only. When the depot's base is
not one (a remote's branch such as `origin/main`), the page says so before
the import runs: every approved bead would be held
([line.md](line.md#4-approved-the-assembly-lands-it-on-main)).

### The test command

For a git depot with no test command, the import sets one when the
repository says how it is tested: a test script of its own (`.yardr/test`,
`scripts/test`), `go.mod`, a `package.json` with a test script, `Cargo.toml`,
a Makefile with a `test` target. It is a guess, named in the import's last
question and printed when the import is done, with the command that changes
it; `yardr depot show <depot>` prints it afterwards. A repository that says
none of that gets no test command, and its beads land untested until you set
one ([configuration.md](configuration.md#tests)). Nothing is written into
the checkout either way, and a dir depot gets no guess.

## After each page

It asks how to go on with the step that is next:

- **Keep going here**: the next page.
- **Talk to the yardmaster**: opens [`yardr chat`](chat.md) on the yardmaster
  in this terminal. The yardmaster goes on from the same step: write to it
  what you want next. Leaving the chat (ctrl+c) ends the run with one line,
  how to come back (`yardr chat`) and that the yardmaster keeps running. With
  `yardr setup --no-chat`, and when who runs it is no user of the yard (the
  chat is a user's; it says so, and how to open it as the operator), it prints
  where the yardmaster is in herdr (agent, workspace, tab, pane) and brings
  its pane to the front instead. When the yardmaster is not running it opens
  no chat: it prints what [`yardr doctor`](doctor.md) says of the yard (the
  `FAULT` lines and the yardmaster's line) and how to start it
  (`yardr init --start`).
- **Stop here**: ends; `yardr setup` goes on from that step later.

## Being told when beads land

Not a step either. Where the forms end, before the question below, `yardr
setup` asks a [user](users.md) "Be told when beads land?", which starts at
no. Yes runs `yardr subscribe <you> yardr --done --where depot=<depot>` for the
setup's depot: each bead of it that becomes done is then a notification in
your inbox, apart from your mail
([subscriptions.md](subscriptions.md#users-as-subscribers)). It is not
asked of who already has a `--done` subscription to yardr, nor of who is
no user of the yard. The onboarding does not ask it.

## Linking with another yard

Not a step: a yard is complete without it. Where the forms end (every step
done, or the first bead on its way) `yardr setup` asks "Link with another
yard?", which starts at no. Yes opens the form of `yardr peer pair`
([peers.md](peers.md#the-form)): one yard shows a code, the other enters it,
both operators compare a number, and each says what the link may carry and
whom of their yard the other may write to. The
onboarding asks the same once the yardmaster is up, after the daemon at login
and the web view.

It is asked once a run, of the operator (only the operator pairs), and not
on a yard that has a peer. A pairing that fails says why and the setup goes
on; `yardr peer pair` in a terminal opens the form at any time.

## Cancelling

A page that is cancelled (ctrl+c, or no at its last question) has run none
of its commands: the yard is as the finished pages made it, and
`yardr setup` goes on from the same step. Leaving the import, at any of its
questions or with a no at the last, has written nothing: no group, no file
under the home, no test command. The forms then stop and print the step and its
command rather than ask again.

A command that fails ends the run with its error; what the page ran before
it stays done.

## Where no form runs

Without a terminal, with `--json`, and in an agent's own session
(`YARDR_SESSION` or `YARDR_CREW` is set: the yardmaster's pane is a terminal
too) `yardr setup` asks nothing. It prints the steps and the command for the
next one; `--json` prints `home`, `steps` and `next`, as
[`yardr doctor --json`](doctor.md#json) names them.

On a home with no yard it is refused like any other command (exit 2), or, in
a terminal, runs the onboarding first.

## As a script

Scripts skip the forms and the yardmaster's conversation (`yardr init --help`):

```sh
yardr init --harness claude [--yardmaster yardmaster] [--dir ~/projects] [--model opus] [--operator Jo] [--style show] [--about "..."]
yardr depot add webapp ~/projects/webapp --base main

# the depot's way of working, from the yard's default pack (starter, unless
# yardr settings set default_pack names another): the groups that build, review
# and land, their roles, the flow and the routes go into the yard, and nothing
# into the repository; the depot's test command is guessed from it
yardr depot init webapp --yes --bind builders=claude:opus --bind reviewers=codex \
  --members backlog= --members decisions=jo
yardr depot show webapp                 # test: what the builders, the reviewers and the assembly run

yardr daemon &                          # routes and supervises; starts the yardmaster (or: yardr init --start)
yardr watch                             # live event stream
yardr session list --running            # what agents and scripts work on now; without the flag, also the beads a manual group holds for a person
yardr bead list -a                      # closed beads too: the newest 200 (-n <N> for more, --all for every one); session list -a likewise

yardr bead create "Add login page" --depot webapp -t feature -b "Use the OAuth flow"
# it starts in the flow's first stage, backlog; the yardmaster shapes it and advances it to new
```

From there the bead travels its depot's flow: the starter pack's, the one
`yardr depot init` sets a depot up with, is drawn and read in
[flows.md](flows.md#drawing-a-flow) (`yardr flow draw starter`), and
[line.md](line.md) follows a bead through it.
