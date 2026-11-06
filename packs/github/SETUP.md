# Setting up GitHub

The pack wires GitHub into the yard as the declared source
`github`: its webhooks are verified and read, and the yard
replies, opens issues and sets their status through the commands the pack
carries. What a stage means in GitHub is a flow pack's to say, not this
file's.

1. **The webhook, on the repository.** Settings → Webhooks → Add webhook:
   the URL `https://<public host>/hooks/<hook name>` — the path ends in
   the hook's name, the one `yardr hook add` is given below — content type
   `application/json`, a secret, and the events Issues, Issue comments,
   Pull requests, Pull request reviews, Workflow runs and Check suites.
   The yard reads those deliveries; anything else it cannot use.
2. **The hook and its secret.** The webhook's secret goes in a file, mode
   0600, which the hook names. A new file takes its mode from the
   shell's umask, so narrow it first. The secret is never a command's
   word: that line would stay in the shell's history. Each command below
   is one thing to type, in a block of its own; run the second, type or
   paste the secret blind and press Enter:
   ```sh
   umask 077
   ```
   ```sh
   IFS= read -rs v && printf '%s' "$v" > ~/.yardr/github.secret; unset v
   ```
   ```sh
   chmod 600 ~/.yardr/github.secret
   ```
   ```sh
   yardr hook add gh --source github --secret-file ~/.yardr/github.secret
   # hook gh saved: POST /hooks/gh (github)
   ```
   One hook serves every crew member.

   A crew member sets the hook up by two asks on one bead, the secret
   first (`--secret-hook gh --source github`, then `--run "hook add gh
   --source github"`): the operator pastes the secret typed unseen, the
   server keeps it in `<home>/hooks/gh.secret` (mode 0600), and
   approving the hook makes `POST /hooks/gh`. The manual steps above
   remain available; `--map`, `--says` and `--as` stay in a terminal.
3. **The crew member's credential.** A token in an env file of the crew
   member's own, mode 0600, never in the store. Have the crew member ask
   for it, and answer on its card, in footplate or in `yardr chat`
   (`ctrl+o`), where the field shows dots:
   ```sh
   yardr bead ask <bead> "The token it answers with" --secret github --for-crew <name> --env GITHUB_TOKEN --context "Make a fine-grained token for this one repository: on GitHub, open Settings, then Developer settings, then Fine-grained tokens, and make a token for this repository with Contents set to read and write, and Issues and Pull requests set to read and write."
   ```
   The yard's server writes it into the crew member's env file, or with
   none set into `~/.yardr/crew/<name>.env`, which is then set as `yardr
   crew set` sets it. The running session does not see the new value:
   restart the crew member after giving it. By hand it is yours instead.
   A new file takes its mode from the shell's umask, so narrow it first.
   The token is never a command's word: that line would stay in the
   shell's history. Each command below is one thing to type, in a block
   of its own; run the second, type or paste the token blind and press
   Enter:
   ```sh
   umask 077
   ```
   ```sh
   IFS= read -rs v && printf 'GITHUB_TOKEN=%s' "$v" > ~/.yardr/crew/<name>.env; unset v
   ```
   ```sh
   chmod 600 ~/.yardr/crew/<name>.env
   ```
   ```sh
   yardr crew set <name> env_file=~/.yardr/crew/<name>.env
   ```
   The token reads and writes the repository's issues and pull requests;
   a token that may only read answers nothing. Restart the crew member
   after changing the file. Or the member speaks as a GitHub App: the
   App's id and key file beside the token's place, and the yard mints an
   installation token narrowed to each repository from them, with the
   umask narrowed first as above. The id and the key file's path are
   typed as usual: they are no secret, and the key itself is never
   typed:
   ```sh
   umask 077
   ```
   ```sh
   printf '%s\n' 'GITHUB_APP_ID=<app id>' 'GITHUB_APP_PRIVATE_KEY_FILE=<key file>' > ~/.yardr/crew/<name>.env
   chmod 600 ~/.yardr/crew/<name>.env
   yardr crew set <name> env_file=~/.yardr/crew/<name>.env
   install contrib/github-mint/github-mint ~/bin/    # from main
   yardr source set github mint_command=$HOME/bin/github-mint
   # source github: mint command /Users/me/bin/github-mint
   ```
   `yardr source check github` looks the setup over — the
   source's own env file, the mint, and that every thread command runs —
   and sends nothing:
   ```sh
   yardr source set github env_file=~/.yardr/crew/<name>.env
   # source github: GITHUB_TOKEN is set in /Users/me/.yardr/crew/<name>.env
   yardr source check github
   # github: credential: GITHUB_TOKEN is set in /Users/me/.yardr/crew/<name>.env
   # github: its file names no check call (check: <a read call without parameters>), so nothing was sent
   # github: command thread.reply (github/thread reply): can be run
   # ...
   ```
   The source's file may name the crew member's where one member is all
   the yard has; a yard with several keeps its own, with the App's names
   where it mints.
4. **Its identity.** The login of the user the token acts as, looked up on
   GitHub by hand — or the App's bot, where it speaks as one:
   ```sh
   yardr crew identity <name> --source github --key <login>
   yardr crew identity <name> --source github --key '<slug>[bot]'
   ```
   A crew member setting itself up proposes the login instead, from its
   session, and the operator approves it on the card:
   ```sh
   yardr bead ask <bead> "Who am I on GitHub?" --run "crew identity <name> --source github --key <login>"
   ```
   A bot login (`<slug>[bot]`) has characters no run ask carries, so it
   stays typed. A login is kept lowercase, however it was typed. What the
   bot does is then its own, and @\<slug\> in a comment mentions it.
5. **The grants.** Opening issues and setting their status are the
   operator's to grant:
   ```sh
   yardr crew set <name> open_threads=github set_state=github
   ```
6. **The daemon's reads.** Judging a run against the pull request's head
   and reading CI as state run with the hook's crew member's credential,
   read from the member's env file (step 3) as for a move; nothing ambient
   is tried. The hook acts as that member, and adding the member re-saves
   the hook, so the source and secret go again:
   ```sh
   yardr hook add gh --source github --secret-file ~/.yardr/github.secret --as <name> \
     --says "event=ci conclusion=success stage=in-pr -> done" \
     --says "event=ci conclusion=failure stage=in-pr -> changes"
   # hook gh saved: POST /hooks/gh (github)
   ```
   The lines are the flow's (`yardr docs line` writes them for its train flow):
   a hook with an `event=ci` line and no `--as` is refused, as is one
   whose source declares no `ci` command. A hook whose table has no
   `event=ci` line may skip the `--as`: events still move beads, and only
   the live reads need the member. The train's brief carries the same read
   from the source's own env file (step 3), since the brief has no hook.
7. **Test it.** As the crew member: open an issue, mention it from GitHub,
   move it:
   ```sh
   yardr thread open github --in <owner>/<repo> - -b -   # stdin: the title (Hello from the yard), then the body (testing the wiring)
   # comment @<login> on the issue in GitHub: it lands in the crew member's inbox as mentioned
   yardr thread state <owner>/<repo>#<n> started
   ```
   `yardr thread list <name>` shows the thread's id.
   To open an issue assigned to a person, give the yard's user their
   login once, then name them. GitHub requires the credential to have push
   access to set assignees; the command reports an error if GitHub creates
   the issue but drops the assignee:
   ```sh
   yardr user identity <user> --source github --key <their login>
   yardr thread open github --in <owner>/<repo> --assign <user> -   # stdin: the title (For you)
   ```
8. **Pushing the depot's landings.** A depot whose origin is on github.com
   pushes its landings because git itself is handed the source's token.
   The pack brings the credential helper; the depot's git is pointed at
   the copy the import made, in that depot's own config alone:
   ```sh
   git -C <depot> config credential."https://github.com".helper ~/.yardr/sources/github/github/git-credential
   ```
   Never `--global`: the person's own login stays as it is for everything
   else (on a Mac, the keychain), and only this depot's git asks the
   helper. Only the helper's name stands in the config: the token is in no
   remote URL, group `cmd`, log or config, and is read from the source's
   env file at the moment git asks, so a rotated token needs no re-turn-on.
   The token needs the `contents: write` permission to push; where the
   source mints from a GitHub App instead of a static token, add a
   `GITHUB_TOKEN` with that permission to the source's env file for pushes
   (the yard keeps minting for its own calls). Whether the depot pushes at
   all is the assembly group's `YARDR_PUSH`, not the pack's.
