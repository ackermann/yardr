# For an agent that installs or sets up yardr

You are installing yardr for a person, or taking their yard from installed
to a first bead in work. The person decides; you run the commands.
[install.md](install.md) has the install itself, and its part
[On a server](install.md#on-a-server-no-desktop) the order for a machine
without a desktop.

## The loop

`yardr doctor` is the source of what is missing and what is next. It runs
before there is a yard, changes nothing, and exits 1 while something the
yard needs is missing.

1. Run `yardr doctor --json`.
2. `faults` is not empty: fix the first one. Each says what is missing and
   how to get it.
3. `faults` is empty: `.next.next` is the command for the next setup step
   (`yardr rig add <rig> <path> --base <branch>`), `.next.detail` what is
   lacking. Fill in the placeholders from the person's answers and run it.
4. Run doctor again. Done when `faults` is empty and `next` is absent, or
   when the step left is `merged_bead`: that one is the pipeline's.

Take every step from doctor's output, in the order it gives them: it reads
the yard as it stands now, whoever did the earlier steps.
[doctor.md](doctor.md) explains each line.

## Ask the person

Stop and ask for each of these; an answer you made up is a yard set up
wrong:

- **Which repository becomes a rig, under which name, on which base
  branch.** With no rig yet, "name the repository" is the right place to
  stop.
- **Anything that lets an agent act unasked**: a group's or resident's
  permission flags (`yardr group add --help` lists them per harness; `yardr
  init` gives the mayor its harness's), `YARDR_PUSH`, a credential file.
- **Logins**: the harness's, `gh`'s. The person logs in; you check the
  result with `yardr doctor --try`, which makes one real call to each
  harness and costs a model call each. Ask before you run it.
- **Who the operator is** (`yardr init --operator <name>`) and which
  harness the yard runs on (`--harness <kind>`).

## Commands, and their wrong guesses

| you want | run | not |
|---|---|---|
| which build | `yardr version`; `yardr --version` and `-v` print the same | `-version` |
| what is missing, what is next | `yardr doctor`, `--json` | `yardr status`, `yardr check` |
| a yard, without a form | `yardr init --harness <kind> --operator <name> --start` | bare `yardr`, `yardr init` without `--harness` |
| a yard in another directory | `--home <dir>` on any command | a variable of your own |
| the daemon as a service | `yardr daemon install`, from a login shell | a unit you wrote; `--file-only` writes the file and starts nothing |
| herdr's server as a service | [install.md](install.md#herdrs-server) | `yardr daemon install`, which is yardr's daemon only |
| a message to the mayor | `yardr mail mayor "<text>"`; `yardr mail --with mayor` reads the conversation | `yardr chat` |

Every command takes `--json` and `--help`. Check a flag with `--help`
before you use it.

## Without a terminal

You have no terminal, so no form opens. What each command does then:

| command | in a terminal | without one |
|---|---|---|
| `yardr` | no yard: the onboarding form; a yard: help | no yard: exit 2, naming `yardr init --harness <kind>`; a yard: help |
| `yardr init` | no form either way | the same; `--start` opens no chat |
| `yardr setup` | a form per step | prints the steps and the next command, as doctor does |
| `yardr chat` | the view | exit 1, naming `yardr mail` |
| `yardr group add`, `yardr resident add` (no name) | a form | exit 1: give the name and flags |
| `yardr review` | a form | lists the beads |
| any command on a home with no yard, but `doctor`, `init`, `version`, `settings` | the onboarding first | exit 2 |

`YARDR_SESSION` or `YARDR_RESIDENT` in your environment has the same effect
as no terminal for `setup` and `init --start`.

Close stdin when you call a harness yourself (`codex exec … < /dev/null`):
one that waits for a login or a question then ends with an error you can
read, where it would otherwise hang.

## Where things are

| what | where |
|---|---|
| the yard | its home: `--home <dir>`, else `YARDR_HOME`, else `~/.yardr` |
| the store | `<home>/yard.db`; read it through commands, never the file |
| logs | `<home>/server.log`, `<home>/daemon.log` (a daemon in the background or as a service) |
| the mayor: running, failed starts and why | `yardr resident show mayor`, `yardr resident list` |
| what happened, in order | `yardr events` (`-n <count>`, `--bead <id>`) |
| the mayor's pane | `herdr agent attach <yard>-mayor`, in a terminal; `herdr agent list` names the agents |
| is herdr's server up | `herdr status server` |
| the service's file and state | `yardr daemon status` |
