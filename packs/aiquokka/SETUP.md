# Setting up aiquokka

aiquokka reads the usage limits of the subscriptions the agents' CLIs are
logged in to. The yard asks it through a declared source named `aiquokka`:
`yardr limits <kind>` prints the windows it reports, and a route's condition
gates builders on them. Docs has the longer telling: `yardr docs
integrations` (the section on what a subscription still carries) and
`yardr docs groups` (the section on quota conditions on a route).

This directory is no pack, so there is no `yardr pack apply aiquokka`. A
pack may not bring a `limits` key: the yard runs the command it names, so
adding one is the operator's to approve. The directory holds the files of
the source, which `yardr source add` takes: `aiquokka.yaml`, the
declaration, and `aiquokka`, its command file, which runs the `aiquokka`
found on PATH. Both are in a yardr checkout (github.com/yardr-dev/yardr)
as `packs/aiquokka`.

1. **Install aiquokka.** From its GitHub releases page
   (github.com/McKean/aiquokka/releases): download the release for your
   platform and put its `aiquokka` binary on PATH. If the page has none
   for your platform, build it with Go:
   ```sh
   go install github.com/McKean/aiquokka@latest
   ```
   Either way `aiquokka` must be found on the PATH of whoever runs the
   yard: the daemon's PATH need not be the shell's, so a binary under
   `~/go/bin` wants that directory on the daemon's PATH too.
2. **Check it.** With each CLI whose limits the yard should read logged
   in, as that CLI logs in:
   ```sh
   aiquokka claude --json
   ```
   prints one JSON object with the provider's windows. Ask each provider
   the yard burns the same way (`aiquokka codex --json`, and so on for
   grok, kimi and muse). A provider whose CLI is not logged in answers
   nothing: the yard reads no windows for it until it is.
3. **Add the source.** A crew member proposes it and the operator
   approves the card. Run from a yardr checkout:
   ```sh
   yardr bead ask <bead> "Add the aiquokka limits source?" \
     --context "Routes can then gate builders on token use, as builders@Weekly<90." \
     --run "source add packs/aiquokka/aiquokka.yaml"
   ```
   The card shows the declaration and the command file whole, as they
   will be added. It is weighty, since the file is a program the yard
   will run: the operator approves it in footplate, or in a terminal on
   the yard's machine with `yardr bead answer <bead> <ask> --approve`.
   The path is read from the directory the command runs in. From
   elsewhere give the file's whole path; the yard splits the command at
   spaces and nowhere else, so a path with a space in it is the
   operator's to type, as follows.

   Or the operator adds it by hand:
   ```sh
   yardr source add packs/aiquokka/aiquokka.yaml
   # source aiquokka saved: no call, its limits command alone
   #   limits: its command says what the subscription of claude, codex, grok, kimi, muse still carries (yardr limits <kind>)
   # next: yardr source check aiquokka (it asks for each of those kinds)
   #   the commands' files are under ~/.yardr/sources/aiquokka, each mode 0700: aiquokka
   ```
   `yardr source show aiquokka` prints it as the yard reads it. The
   source asks for all five kinds, and `yardr source check aiquokka` fails
   for one whose CLI is not logged in on this machine, which only means
   that kind is not read. To read fewer, delete their lines under `kinds`
   in a copy of `aiquokka.yaml` (the command file goes beside it) and add
   the copy. A source named `aiquokka` that the yard already has is
   replaced by the operator in a terminal, never by a card.
4. **Read the windows.**
   ```sh
   yardr limits claude
   # claude  aiquokka says (provider Claude):
   #   5h            24% used  resets 19:00 (in 33m)
   #   Weekly        45% used  resets Thu 15 Oct 14:27 (in 2d 20h)
   #   Weekly Fable  67% used  resets Thu 15 Oct 14:27 (in 2d 20h)  model Fable
   #   no window is used up
   ```
   prints the windows with the labels a condition names. With `--group`
   the command runs with that group's env, as the dispatcher asks it:
   ```sh
   yardr limits --group builders claude
   ```
   A report of `exit status 127` with `exec: aiquokka: not found` means
   the PATH of whoever asked lacks the binary (step 1).
5. **Gate a route on them.** The steps use `builders` as the group that
   starts agents and `web` as the depot; put this yard's names there.
   ```sh
   yardr route add --stage new --depot web --group 'builders@Weekly<90'
   ```
   A bead of this route goes to `builders` while the window its provider
   labels `Weekly` is under 90% used. The label is matched exactly, so
   read it from step 4's report; a window the condition does not name
   does not gate.

The source names no login of its own: the command runs with the env of
whoever asks, so a group on its own login gates on its own windows. Where
the muse login the yard should read lives in a file of its own, set
`MUSE_AUTH_PATH` to it in the env of the group that burns muse, in that
group's env_file (`yardr group set <group> env_file=<file>`).
`yardr limits --group <group> muse` reads the same.
