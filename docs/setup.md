# yardr setup

`yardr setup` takes a yard from its onboarding to a first bead in work, as a
short run of forms in the terminal. It is the same setup the mayor does with
you in conversation, and you can change from one to the other after any step.

```sh
yardr setup
yardr setup --json | jq -r '.next.next'   # the command for the next step, no form
```

The onboarding ends in it: once the mayor is up, its last question is "Talk
to the mayor" or "Keep going here", and "Keep going here" is `yardr setup` in
the same run.

## One list of steps

The steps are the ones [`yardr doctor`](doctor.md#setup) prints, read from
the store by one function. `yardr setup` prints them, each `done` or `todo`,
and takes the first one not done. It keeps no state of its own, so it goes on
where the yard stands, whoever did the steps before: these forms, the mayor
in its pane, or you on the command line. Run it again at any time.

The mayor's first run reads the same list through `yardr doctor`. There is no
second list to keep in step with it.

## The pages

One page per step. Each asks what its command needs, shows the command line
as you would type it (after `$`, as `yardr --home <home> …` for a home other
than the default) and runs that command.

On a yard whose `operator_style` is `guide`
([configuration.md](configuration.md#how-agents-work-with-you)) the pages
print no command lines: the questions and what each command prints say what
happens, the group page's summary has no "As a command:", and the routes are
asked in words (`stage new of webapp goes to webapp/builders`). The commands
run are the same. With any other style, and with none, the pages show them.

| step | the page asks | and runs |
|---|---|---|
| a rig | the repository's path (it starts at the directory you are in, when that is a checkout's top), the rig's name (the directory's, in lowercase), the base branch (the branch checked out) | `yardr rig add <rig> <path> --base <branch>`. A directory that is no git checkout is asked for no base and becomes a dir rig |
| the rig's `.yardr` files on its base | whether to commit, after `yardr rig init` has run and said what it wrote and which gate it guessed | `yardr rig init <rig>`, then `git -C <path> add .yardr && git -C <path> commit -m '…' -- .yardr`: only `.yardr/` is committed, whatever else is staged |
| a group that builds | the questions of `yardr group add`'s form (name, runner, agent, model, permissions, prompt, limit), starting at `<rig>/builders` on the yard's `default_kind`, with its unattended flags, `roles/builder.md` and limit 2 | `yardr group add <rig>/builders --runner herdr --limit 2 --set …`. The command is on the form's last page, "Save this group?" |
| a group that reviews | the same, starting at `<rig>/reviewers`, `roles/reviewer.md`, limit 1 | `yardr group add <rig>/reviewers --runner herdr --set …` |
| a group or person that lands approved beads | a refinery, or by hand; for by hand, who merges | see below |
| routes for new, review and approved | nothing but a yes: each stage goes to the group that does its work, and the commands are shown | `yardr route add --stage <stage> --rig <rig> --group <group>` for each stage without a route |
| a first bead | its title and its goal | `yardr bead create "<title>" --rig <rig> -b "<goal>"`, then `yardr bead advance <id> --to new` |

The last step, a first merged bead, has no page: it is the pipeline's to do.
There the forms end and say where the first bead is and how to watch it:
`yardr session list`, `yardr bead show <id>`, `yardr watch`, and
`yardr web serve` for the yard in a browser ([web.md](web.md); once the web
view is installed as a login service, its address instead). They also say
so when no daemon is running, since nothing is dispatched then.

### Committing the rig's files

yardr reads a rig's roles, gate and flow from its base branch, not from the
files in the checkout, so the step is done only once `.yardr/` is committed
there. The page commits when you say yes and the base is the branch checked
out in the rig.

Answer no to commit by hand: the files stay in the checkout, the page prints
the command, and the setup waits at this step. Read the files, edit the gate
(`.yardr/check` is a guess from the repository), commit, and `yardr setup`
goes on. When the base is not the branch checked out (a remote's branch such
as `origin/main`, or another local branch), the page does not offer to commit
and says where the files have to get to.

### How approved beads land

**A refinery** is one answer. The page writes `merge-local`, as this yardr
was built with it, to `<home>/scripts/merge-local` (mode 0755) and adds the
group:

```sh
yardr group add <rig>/refinery --runner exec \
  --set cmd=<home>/scripts/merge-local --set log=<home>/logs/refinery.log
```

A `merge-local` already there is kept as it is, this build's or not. The
group pushes nothing; put `YARDR_PUSH=origin` in front of its `cmd` to push
the base after each landing ([pipeline.md](pipeline.md#4-approved-the-refinery-lands-it-on-main)).
merge-local lands on local branches only: when the rig's base is not one, the
question starts at "by hand" and says so.

**By hand** asks who merges (it starts at you) and runs two commands, the
group and its route, since a group of people shows as landing only once it is
routed:

```sh
yardr group add <rig>/mergers --runner manual --members <them>
yardr route add --stage approved --rig <rig> --group <rig>/mergers
```

## After each page

It asks how to go on with the step that is next:

- **Keep going here**: the next page.
- **Talk to the mayor**: prints where the mayor is in herdr (agent,
  workspace, tab, pane), brings its pane to the front and ends. The mayor
  goes on from the same step. When the mayor is not running it says how to
  start the yard (`yardr init --start`).
- **Stop here**: ends; `yardr setup` goes on from that step later.

## Cancelling

A page that is cancelled (ctrl+c, or no at its last question) has run none
of its commands: the yard is as the finished pages made it, and
`yardr setup` goes on from the same step. One thing is left behind by a
cancelled page: `yardr rig init` writes the rig's files into the checkout
before the question whether to commit them. They stay there, uncommitted, and
rig init never overwrites a file, so the page can be taken again.

A command that fails ends the run with its error; what the page ran before
it stays done.

When a page's answers leave its step undone (a builders' group whose prompt
is not `roles/builder.md`, which is how the step is told), the forms stop and
print the step and its command rather than ask again.

## Where no form runs

Without a terminal, with `--json`, and in an agent's own session
(`YARDR_SESSION` or `YARDR_RESIDENT` is set: the mayor's pane is a terminal
too) `yardr setup` asks nothing. It prints the steps and the command for the
next one; `--json` prints `home`, `steps` and `next`, as
[`yardr doctor --json`](doctor.md#json) names them.

On a home with no yard it is refused like any other command (exit 2), or, in
a terminal, runs the onboarding first.
