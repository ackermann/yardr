# Setting up GitHub

The pack wires GitHub into the yard as the declared source
`github-declared`: its webhooks are verified and read, and the yard
replies, opens issues and sets their status through the commands the pack
carries. What a stage means in GitHub is a flow pack's to say, not this
file's.

1. **The webhook, on the repository.** Settings → Webhooks → Add webhook:
   the URL `https://<public host>/hooks/<hook name>` — the path ends in
   the hook's name, the one `yardr hook add` is given below — content type
   `application/json`, a secret, and the events Issues, Issue comments,
   Pull requests and Pull request reviews. The yard reads those
   deliveries; anything else it cannot use. Workflow runs and check suites
   arrive with the pack's second half, which judges CI.
2. **The hook and its secret.** The webhook's secret goes in a file, mode
   0600, which the hook names:
   ```sh
   printf '%s' '<secret>' > ~/.yardr/github.secret
   chmod 600 ~/.yardr/github.secret
   yardr hook add gh --source github-declared --secret-file ~/.yardr/github.secret
   # hook gh saved: POST /hooks/gh (github-declared)
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
   yardr source set github-declared mint_command=$HOME/bin/github-mint
   # source github-declared: mint command /Users/me/bin/github-mint
   ```
   Either way `yardr source check github-declared` looks at the
   credential and the thread commands, and sends nothing:
   ```sh
   yardr source check github-declared
   # github-declared: credential: GITHUB_TOKEN is set in ~/.yardr/crew/<name>.env
   ```
4. **Its identity.** The login of the user the token acts as, looked up on
   GitHub by hand — or the App's bot, where it speaks as one:
   ```sh
   yardr crew identity <name> --source github-declared --key <login>
   yardr crew identity <name> --source github-declared --key '<slug>[bot]'
   ```
   A login is kept lowercase, however it was typed. What the bot does is
   then its own, and @\<slug\> in a comment mentions it.
5. **The grants.** Opening issues and setting their status are the
   operator's to grant:
   ```sh
   yardr crew set <name> open_threads=github-declared set_state=github-declared
   ```
6. **Test it.** As the crew member: open an issue, mention it from GitHub,
   move it:
   ```sh
   yardr thread open github-declared --in <owner>/<repo> "Hello from the yard" -b "testing the wiring"
   # comment @<login> on the issue in GitHub: it lands in the crew member's inbox as mentioned
   yardr thread state <owner>/<repo>#<n> started
   ```
   `yardr thread list <name>` shows the thread's id.
