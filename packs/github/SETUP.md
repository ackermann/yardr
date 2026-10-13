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
   0600, which the hook names:
   ```sh
   printf '%s' '<secret>' > ~/.yardr/github.secret
   chmod 600 ~/.yardr/github.secret
   yardr hook add gh --source github --secret-file ~/.yardr/github.secret
   # hook gh saved: POST /hooks/gh (github)
   ```
   One hook serves every crew member.
3. **The crew member's credential.** A token in an env file of the crew
   member's own, mode 0600, never in the store:
   ```sh
   printf '%s' 'GITHUB_TOKEN=<token>' > ~/.yardr/crew/<name>.env
   chmod 600 ~/.yardr/crew/<name>.env
   yardr crew set <name> env_file=~/.yardr/crew/<name>.env
   ```
   The token reads and writes the repository's issues and pull requests;
   a token that may only read answers nothing. Restart the crew member
   after changing the file. Or the member speaks as a GitHub App: the
   App's id and key file beside the token's place, and the yard mints an
   installation token narrowed to each repository from them:
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
   A login is kept lowercase, however it was typed. What the bot does is
   then its own, and @\<slug\> in a comment mentions it.
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
   The lines are the flow's (docs/line.md writes them for its train flow):
   a hook with an `event=ci` line and no `--as` is refused, as is one
   whose source declares no `ci` command. A hook whose table has no
   `event=ci` line may skip the `--as`: events still move beads, and only
   the live reads need the member. The train's brief carries the same read
   from the source's own env file (step 3), since the brief has no hook.
7. **Test it.** As the crew member: open an issue, mention it from GitHub,
   move it:
   ```sh
   yardr thread open github --in <owner>/<repo> "Hello from the yard" -b "testing the wiring"
   # comment @<login> on the issue in GitHub: it lands in the crew member's inbox as mentioned
   yardr thread state <owner>/<repo>#<n> started
   ```
   `yardr thread list <name>` shows the thread's id.
   To open an issue assigned to a person, give the yard's user their
   login once, then name them:
   ```sh
   yardr user identity <user> --source github --key <their login>
   yardr thread open github --in <owner>/<repo> --assign <user> "For you"
   ```
