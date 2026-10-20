# Setting up PostHog

The pack wires PostHog into the yard as the declared source `posthog`:
an agent reads it through `yardr call` (HogQL queries, error tracking
issues), and a webhook hears a new error issue. Nothing here writes to
PostHog: every call is a read, and resolving or assigning an issue stays
in PostHog's pages.

1. **A personal API key.** In PostHog, your user settings → Personal API
   keys (Settings → Personal API keys,
   `https://us.posthog.com/settings/user-api-keys#personal-api-keys`):
   create one with the scopes `query:read`, `error_tracking:read` and
   `user:read`, for the queries, the issues and the check below. The
   yard sends it as `Authorization: Bearer`, read from the variable
   `POSTHOG_API_KEY`, the name and never the value.
2. **The project id.** In PostHog, the project's settings → Project ID
   (`https://us.posthog.com/settings/project#variables`): the number,
   which every call below takes as `project`.
3. **Apply the pack.** As the operator:
   ```sh
   yardr pack apply posthog
   # source posthog is declared: 4 calls under https://us.posthog.com (yardr source show posthog)
   ```
4. **Give the yard the key.** No file is written by hand: the command
   asks for the key and shows nothing as it is typed or pasted, and the
   yard's server writes it to a file only you can read
   (`~/.yardr/sources/posthog.env`).
   ```sh
   yardr source key posthog           # POSTHOG_API_KEY, the personal API key: the operator's to give
   ```
   Or have a crew member ask for it, and answer on its card, in
   footplate or in `yardr chat` (`ctrl+o`), where the field shows dots:
   ```sh
   yardr bead ask <bead> "PostHog's personal API key" --secret posthog --context "Settings → Personal API keys, scopes query:read, error_tracking:read, user:read."
   ```
   The key goes nowhere else: not into mail, chat, a bead or a pack.
5. **Grant the calls and test them.** Reading is the operator's to
   grant, every read call with `read`:
   ```sh
   yardr source grant posthog read --group builders
   yardr source check posthog
   # posthog: check call whoami (GET /api/users/@me/): 200 in …ms, … bytes
   # posthog: email: <the key owner's address>
   ```
   In a session of a granted group, with the project id from step 2:
   ```sh
   yardr call posthog issues project=<id>
   yardr call posthog issue project=<id> id=<an issue's id>
   yardr call posthog query project=<id> hogql="select event, count() from events group by event order by count() desc limit 10"
   ```
   The issues come 25 by default (`limit`, at most 100, `offset` pages);
   the list takes no status filter, so every status comes back. A query
   answers its columns and rows; PostHog refuses `OFFSET` on API keys,
   so page on `timestamp` instead.
6. **The hook and its secret.** The secret is the operator's to make:
   PostHog holds whatever the destination is given, and the yard keeps
   the same in the file the hook names, so make one and put it in both
   places. A new file takes its mode from the shell's umask, so narrow
   it first. Each command below is one thing to type, in a block of its
   own:
   ```sh
   umask 077
   ```
   ```sh
   openssl rand -base64 32 | tr '+/' '-_' | tr -d '=' > ~/.yardr/posthog.secret
   ```
   ```sh
   chmod 600 ~/.yardr/posthog.secret
   ```
   ```sh
   yardr hook add posthog --source posthog --secret-file ~/.yardr/posthog.secret
   # hook posthog saved: POST /hooks/posthog (posthog)
   ```
7. **The destination, in PostHog.** Data pipelines → Destinations → +
   New → Destination: search for "HTTP Webhook" and Create. On its
   page:
   - **Webhook URL**: `https://<public host>/hooks/posthog` — the path
     ends in the hook's name, the one `yardr hook add` was given above.
   - **JSON Body**: the default names the event and the person but no
     project, so add the project beside them:
     ```json
     {"event": "{event}", "person": "{person}", "project": "{project}"}
     ```
   - **Secret headers**: `X-Yardr-Secret` with the secret step 6 made.
     Leave the destination's own Signing secret empty: the yard
     verifies this header, not a signature.
   - **Filters**: match the event `$error_tracking_issue_created`, and
     nothing else: the yard hears a new issue, not a reopened or
     spiking one.
   
   Create & Enable, then Test function: the hook answers 200, which
   proves the URL and the secret.
8. **Hear new issues, and file beads for them.** A subscription hears
   them in an inbox:
   ```sh
   yardr subscribe <hearer> posthog --scope all --where type=error_issue
   ```
   A makes line on the hook files a bead for each new issue instead,
   with the issue's title, its description and a link to it. `hook add`
   replaces the definition, so the secret file is given again, and the
   line names the depot, since the event names no repository:
   ```sh
   yardr hook add posthog --source posthog --secret-file ~/.yardr/posthog.secret --makes "action=created event=error_issue -> task:new@<depot>"
   # hook posthog saved: POST /hooks/posthog (posthog)
   # its events make a bead for a thread none tracks:
   #   action=created event=error_issue -> task:new@<depot>
   ```

On EU Cloud the calls go to `https://eu.posthog.com` instead of
`https://us.posthog.com`: the pack ships the US base, and `yardr source
set` sets no base, so copy the pack's directory from a yardr checkout,
change `base:` in `posthog.yaml`, and add it after the pack is applied
(`yardr source add <copy>/posthog.yaml`, beside its `posthog/`
directory, which the add copies with it). The hook needs no such copy:
the issue's URL is built from the `project.url` the delivery carries,
on whichever host sent it. Applying the pack again replaces the
declaration with the US base, so add the copy again after.
