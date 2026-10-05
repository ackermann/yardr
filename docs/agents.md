# For an agent that installs or sets up yardr

You install yardr for a person or take their yard to a first bead. The person
decides; you run the commands. See [install.md](install.md), including its
[server steps](install.md#on-a-server-no-desktop).

## The loop

Run `yardr doctor --json`. Its `faults` say what is missing; fix the first
one, then run doctor again. When `faults` is empty, `.next.next` is the next
setup command and `.next.detail` says what is lacking. Fill its placeholders
from the person's answers, run it, then run doctor again. Stop when `next` is
absent or its step is `merged_bead` (the pipeline's). Doctor reads the yard
as it stands; follow its order, not a list from the beginning. See
[doctor.md](doctor.md) for the fields and lines.

## Ask the person

- Which repository, rig name, and base branch to use. If there is no rig yet,
  stop at “name the repository.”
- Anything that grants an agent permission: `yardr init`'s harness defaults,
  group or resident flags, `YARDR_PUSH`, credential files.
- Harness and `gh` logins. The person logs in. Ask before `yardr doctor
  --try`: it makes one real call per harness and costs a model call.
- The operator's name and the harness for the yard.

## Commands

| need | use | common wrong guess |
|---|---|---|
| build | `yardr version` (`yardr --version`, `-v` also work) | `yardr -version` |
| missing items and next step | `yardr doctor [--json]` | `yardr check`; `yardr status`, which lists what runs and checks nothing |
| what runs now and where: server, daemon, residents, the web view's URL | `yardr status [--json]` | `yardr doctor`, `pgrep`, `lsof` |
| create a yard without a form | `yardr init --harness <kind> --operator <name> --start` | bare `yardr`; `yardr init` without `--harness` |
| use another yard directory | `--home <dir>` | inventing a yard variable |
| install yardr's service | `yardr daemon install` from a login shell | treating it as herdr's service; `--file-only` starts nothing |
| message the mayor | `yardr mail mayor "<text>"`; read with `yardr mail --with mayor` | `yardr chat` without a terminal |

Herdr's service is separate: follow [install.md](install.md#herdrs-server).
Check a flag with `--help` before using it.

## Without a terminal

`yardr setup` prints the steps and next command instead of opening its forms.
`yardr init` does not open a form; with `--start` it starts and waits for the
mayor but does not open chat. `yardr chat` exits with guidance to use
`yardr mail`. Bare `yardr` shows help in a yard, and without a yard exits 2
with the `yardr init --harness <kind>` command. `yardr group add` and
`yardr resident add` need names and flags instead of opening forms;
`yardr review` lists beads. Harness checks should have stdin closed, for
example `codex exec … < /dev/null`.

## Where things are

The yard is `--home <dir>`, else `$YARDR_HOME`, else `~/.yardr`; its store is
`<home>/yard.db` (read it through yardr). Logs are `<home>/server.log` and
`<home>/daemon.log`. Use `yardr resident show mayor` and `yardr resident
list` for the mayor's state and failed starts; `yardr events` for the event
history (`-n <count>`, `--bead <id>` filter it). `herdr agent attach
<agent>` attaches to its pane in a terminal; `herdr agent list` names agents.
`yardr daemon status` shows the service file and state.
