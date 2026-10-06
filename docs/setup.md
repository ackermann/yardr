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
same run.

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
| the depot's `.yardr` files on its base; a group that builds; a group that reviews; a group or person that lands approved beads; routes for new, review and approved | what the import asks (below), then whether to commit the gate | `yardr depot init <depot>`, which takes the yard's default pack; then `git -C <path> add .yardr/check && git -C <path> commit -m 'yardr: gate' -- .yardr/check` |
| a first bead | its title and its goal | `yardr bead create "<title>" --depot <depot> -b "<goal>"`, then `yardr bead advance <id> --to new` |

The last step, a first merged bead, has no page: it is the pipeline's to do.
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
([pipeline.md](pipeline.md#4-approved-the-assembly-lands-it-on-main)).

### Committing the gate

In a git depot with no gate, the import writes one into the checkout:
`.yardr/check`, a guess from the repository. yardr reads the gate from the
depot's base branch, not from the checkout, so the page then asks "Commit the
gate on `<base>`?". Yes commits that one file, whatever else is staged.

No, or leaving the question, keeps the file uncommitted and prints the
command. The depot is set up either way and the setup goes on, but until
`.yardr/check` is on the base the depot has no gate and no bead of it lands:
`yardr depot check <depot>` fails with "no gate". When the base is not the
branch checked out (a remote's branch, or another local branch), the page
does not offer to commit and says where the file has to get to. A gate that
was there already is kept and not asked about, and a dir depot gets none.

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
both operators compare a number, and each says what the link may carry. The
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
under the home, no gate. The forms then stop and print the step and its
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
