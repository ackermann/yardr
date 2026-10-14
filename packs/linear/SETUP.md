# Setting up Linear

The pack wires Linear into the yard as the declared source `linear`: its
webhooks are verified and read, and the yard replies, opens issues and
sets their status through the commands the pack carries. What a stage
means in Linear is a flow pack's to say, not this file's.

1. **The webhook, in Linear.** Settings → API → Webhooks: add one with the
   URL `https://<public host>/hooks/<hook name>` — the path ends in the
   hook's name, the one `yardr hook add` is given below — and tick the
   events Issues and Comments. The yard reads issue and comment deliveries
   only; anything else it cannot use.
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
   read -rs v && printf '%s' "$v" > ~/.yardr/linear.secret; unset v
   ```
   ```sh
   chmod 600 ~/.yardr/linear.secret
   ```
   ```sh
   yardr hook add linear --source linear --secret-file ~/.yardr/linear.secret
   # hook linear saved: POST /hooks/linear (linear)
   ```
   One hook serves every crew member.
3. **The crew member's key.** `LINEAR_API_KEY` in an env file of the crew
   member's own, mode 0600, never in the store. A new file takes its
   mode from the shell's umask, so narrow it first. The token is never
   a command's word: that line would stay in the shell's history. Each
   command below is one thing to type, in a block of its own; run the
   second, type or paste the token blind and press Enter:
   ```sh
   umask 077
   ```
   ```sh
   read -rs v && printf 'LINEAR_API_KEY=%s' "$v" > ~/.yardr/crew/<name>.env; unset v
   ```
   ```sh
   chmod 600 ~/.yardr/crew/<name>.env
   ```
   ```sh
   yardr crew set <name> env_file=~/.yardr/crew/<name>.env
   ```
   The token is that of a Linear OAuth application made for the crew
   member, so it acts as the application's own user and not as a person; a
   personal key makes it speak as that person. Restart the crew member
   after changing the file.
4. **Its identity.** The id and the profile key of the user the token acts
   as, looked up in Linear by hand — both:
   ```sh
   yardr crew identity <name> --source linear --id <user id> --key <handle>
   ```
   A mention is matched by the handle when the delivery carries markdown
   only, and by the id otherwise: with the id alone, the test of 11 Oct
   saw a mention arrive as `participant` instead of `mentioned`.
5. **The grants.** Opening issues and setting their status are the
   operator's to grant:
   ```sh
   yardr crew set <name> open_threads=linear set_state=linear
   ```
6. **Test it.** As the crew member: open an issue, mention it from Linear,
   move it:
   ```sh
   yardr thread open linear --in <team key> "Hello from the yard" -b "testing the wiring"
   # comment @<handle> on the issue in Linear: it lands in the crew member's inbox as mentioned
   yardr thread state <thread id> started
   ```
   `yardr thread list <name>` shows the thread's id.
   To open an issue assigned to a person, give the yard's user their
   Linear user id once, then name them:
   ```sh
   yardr user identity <user> --source linear --id <their user id>
   yardr thread open linear --in <team key> --assign <user> "For you"
   ```
7. **The stage map.** When a bead enters a stage, the issue it tracks under
   this source moves with it: the hook carries the map, and the crew
   member it acts as. `hook add` replaces the definition, so the whole
   map is given each time:
   ```sh
   yardr hook add linear --source linear --secret-file ~/.yardr/linear.secret --as <name> \
       --map task:new=started --map task:review=review --map task:merged=done
   # moves tracked linear issues as <name>: task:new=started …
   ```
