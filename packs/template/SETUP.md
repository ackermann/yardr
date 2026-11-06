# Setting up acme

The pack wires the service into the yard as the declared source `acme`: its
webhooks are verified and read, and the yard replies, opens issues and sets
their status through the commands the pack carries. What a stage means in
the service is a flow pack's to say, not this file's.

1. **The webhook, in the service.** Make one with the URL
   `https://<public host>/hooks/<hook name>` — the path ends in the hook's
   name, the one `yardr hook add` is given below — and tick the events for
   issues and comments. The yard reads issue and comment deliveries only;
   anything else it cannot use.
2. **The hook and its secret.** The webhook's signing secret goes in a file,
   mode 0600, which the hook names. A new file takes its mode from the
   shell's umask, so narrow it first. The secret is never a command's
   word: that line would stay in the shell's history. Each command below
   is one thing to type, in a block of its own; run the second, type or
   paste the secret blind and press Enter:
   ```sh
   umask 077
   ```
   ```sh
   IFS= read -rs v && printf '%s' "$v" > ~/.yardr/acme.secret; unset v
   ```
   ```sh
   chmod 600 ~/.yardr/acme.secret
   ```
   ```sh
   yardr hook add acme --source acme --secret-file ~/.yardr/acme.secret
   # hook acme saved: POST /hooks/acme (acme)
   ```
   One hook serves every crew member.
3. **The crew member's credential.** Two tokens in an env file of the crew
   member's own, mode 0600, never in the store: one the reads are sent
   with, one the updates are. A new file takes its mode from the shell's
   umask, so narrow it first. Neither token is ever a command's word:
   that line would stay in the shell's history. Each command below is one
   thing to type, in a block of its own; run the second and the third,
   typing or pasting each token blind and pressing Enter:
   ```sh
   umask 077
   ```
   ```sh
   IFS= read -rs v && printf 'ACME_TOKEN=%s\n' "$v" > ~/.yardr/crew/<name>.env; unset v
   ```
   ```sh
   IFS= read -rs v && printf 'ACME_UPDATE_TOKEN=%s\n' "$v" >> ~/.yardr/crew/<name>.env; unset v
   ```
   ```sh
   chmod 600 ~/.yardr/crew/<name>.env
   ```
   ```sh
   yardr crew set <name> env_file=~/.yardr/crew/<name>.env
   ```
   Drop the third command where the declaration dropped `update_env`.
   Restart the crew member after changing the file. Or the member speaks
   with a minted credential: the two mint names beside the tokens' place,
   and the operator's mint command, which turns their values into a
   short-lived token, with the umask narrowed first as above. The id and
   the key file's path are typed as usual: they are no secret, and the
   key itself is never typed:
   ```sh
   umask 077
   ```
   ```sh
   printf '%s\n' 'ACME_APP_ID=<id>' 'ACME_APP_KEY_FILE=<key file>' > ~/.yardr/crew/<name>.env
   chmod 600 ~/.yardr/crew/<name>.env
   yardr crew set <name> env_file=~/.yardr/crew/<name>.env
   yardr source set acme mint_command=<program> [args...]
   # source acme: mint command <program>
   ```
   `yardr source check acme` looks the setup over — the source's own env
   file, the mint, and that every command runs:
   ```sh
   yardr source set acme env_file=~/.yardr/crew/<name>.env
   # source acme: ACME_TOKEN is set in /Users/me/.yardr/crew/<name>.env; for an update, ACME_UPDATE_TOKEN is set in /Users/me/.yardr/crew/<name>.env
   yardr source check acme
   # acme: credential: ACME_TOKEN is set in /Users/me/.yardr/crew/<name>.env
   # acme: check call ping (GET /me): 200 in 12ms, 96 bytes
   # acme: command thread.reply (acme/thread reply): can be run
   # ...
   ```
   The source's file may name the crew member's where one member is all
   the yard has; a yard with several keeps its own.
4. **Its identity.** The id and the key of the user the token acts as,
   looked up in the service by hand — both:
   ```sh
   yardr crew identity <name> --source acme --id <user id> --key <handle>
   ```
   A key is kept lowercase, however it was typed. Drop `--id` where the
   service tells the yard by the handle alone, and `--key` where it tells
   it by the id alone.
5. **The grants.** Opening issues and setting their status are the
   operator's to grant:
   ```sh
   yardr crew set <name> open_threads=acme set_state=acme
   ```
6. **The stage map and the daemon's reads.** Which stage means which status
   is set on the hook, with the crew member the yard acts as. Judging a
   run against the pull request's head and reading CI as state run with
   that member's credential too, read from the member's env file (step 3)
   as for a move; nothing ambient is tried. `hook add` replaces the
   definition, so the source and the secret file go again:
   ```sh
   yardr hook add acme --source acme --secret-file ~/.yardr/acme.secret --as <name> \
       --map task:new=started --map task:review=review --map task:merged=done \
       --says "event=ci conclusion=success stage=in-pr -> done" \
       --says "event=ci conclusion=failure stage=in-pr -> changes"
   # hook acme saved: POST /hooks/acme (acme)
   # moves tracked acme issues as <name>: task:merged=done task:new=started task:review=review
   ```
   A hook with an `event=ci` line and no `--as` is refused, as is one
   whose source declares no `ci` command. A hook whose table has no
   `event=ci` line may skip the `--as`: events still move beads, and only
   the live reads need the member. The train's brief carries the same read
   from the source's own env file (step 3), since the brief has no hook.
7. **Test it.** As the crew member: open an issue, mention it from the
   service, move it:
   ```sh
   yardr thread open acme --in <team> - -b -   # stdin: the title (Hello from the yard), then the body (testing the wiring)
   # comment @<handle> on the issue in the service: it lands in the crew member's inbox as mentioned
   yardr thread state <thread id> started
   ```
   `yardr thread list <name>` shows the thread's id.
   To open an issue assigned to a person, give the yard's user their id in
   the service once, then name them:
   ```sh
   yardr user identity <user> --source acme --id <their user id>
   yardr thread open acme --in <team> --assign <user> -   # stdin: the title (For you)
   ```
