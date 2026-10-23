# Setting up PostHog

The pack wires PostHog into the yard as the declared source `posthog`:
an agent reads it through `yardr call` (HogQL queries, error tracking
issues, session recordings, a session's Replay Vision observations),
and a webhook hears a new error issue. Nothing here writes to
PostHog: every call is a read, and resolving or assigning an issue stays
in PostHog's pages.

1. **A personal API key.** In PostHog, your user settings → Personal API
   keys (Settings → Personal API keys,
   `https://us.posthog.com/settings/user-api-keys#personal-api-keys`, or
   `https://eu.posthog.com/settings/user-api-keys#personal-api-keys` on
   EU Cloud): create one with the scopes `query:read`,
   `error_tracking:read`, `user:read`, `session_recording:read` and
   `replay_scanner:read`, for the queries, the issues, the check, the
   recordings and the observations below. The yard sends it as
   `Authorization: Bearer`, read from the variable `POSTHOG_API_KEY`,
   the name and never the value.
2. **The project id.** In PostHog, the project's settings → Project ID
   (`https://us.posthog.com/settings/project#variables`, or
   `https://eu.posthog.com/settings/project#variables` on EU Cloud):
   the number, which every call below takes as `project`.
3. **Apply the pack.** As the operator:
   ```sh
   yardr pack apply posthog
   # source posthog is declared: 7 calls under https://us.posthog.com (yardr source show posthog)
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
   yardr bead ask <bead> "PostHog's personal API key" --secret posthog --context "Settings → Personal API keys, scopes query:read, error_tracking:read, user:read, session_recording:read, replay_scanner:read."
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
   yardr call posthog recordings project=<id>
   yardr call posthog recording project=<id> id=<a session's id>
   yardr call posthog observations project=<id> session_id=<a session's id>
   ```
   The issues come 25 by default (`limit`, at most 100, `offset` pages);
   the list takes no status filter, so every status comes back. A query
   answers its columns and rows; PostHog refuses `OFFSET` on API keys,
   so page on `timestamp` instead. The recordings come 50 by default
   (`limit`, at most 50, `after` pages with the previous answer's
   `next_cursor`); `person_uuid`, `session_ids` (a JSON list, such as
   `["<a session's id>"]`), `date_from` and `date_to` narrow them.
   An issue's sessions come through `query`: every `$exception` carries
   its issue's id, so one query names the sessions behind an issue:
   ```sh
   yardr call posthog query project=<id> hogql="select distinct properties.\$session_id from events where event='\$exception' and properties.\$exception_issue_id='<an issue's id>'"
   ```
   Then `observations` reads what PostHog already wrote about one of
   those sessions in words, and the replay link is where a person
   watches it:
   ```sh
   yardr call posthog observations project=<id> session_id=<a session's id>
   # https://us.posthog.com/project/<id>/replay/<a session's id>
   ```
   (`?t=<seconds>` starts the replay at an offset.) Observations exist
   only for sessions a Replay Vision scanner has run on: a summarizer
   scanner is set up in PostHog, where running it costs PostHog
   credits, and reading what it wrote costs nothing here. The pack
   never asks PostHog to run one.
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
   - **Headers**: add `X-Yardr-Secret` with the secret step 6 made. The
     destination sends this header as-is; the yard verifies it instead
     of a signature.
   - **Filters**: match the event `$error_tracking_issue_created`, and
     nothing else: the yard hears a new issue, not a reopened or
     spiking one.

   Create & Enable. Then prove the issue path before a real issue
   arrives: on the destination's page open Testing (Start testing)
   and replace the `event` object in the globals editor with an
   issue-created event, leaving the rest of the globals as shown:

   ```json
   {
     "uuid": "<a fresh uuid>",
     "event": "$error_tracking_issue_created",
     "distinct_id": "<a fresh uuid>",
     "timestamp": "2026-10-23T12:00:00.000000+00:00",
     "properties": {
       "name": "PostHog destination test issue",
       "description": "Sent from the destination's Testing panel; not a real issue.",
       "exception_props": {
         "$session_id": "<a fresh uuid>"
       }
     }
   }
   ```

   Make each uuid fresh (`uuidgen`): the delivery id is the event's
   uuid, so a second run with the same one is kept as a retry of
   the first. Leave Make real HTTP requests on, and Test function:
   the destination POSTs the pasted event through the JSON body
   above, the hook answers 200, and a subscription with `--where
   type=error_issue` hears it, with the session and its replay link
   in its fields. It arrives as a genuine error_issue event — that
   is the point — marked as a test only by its name and
   description. If the panel reports the function was skipped, the
   pasted event did not match the destination's filters. A bare
   Test function without the paste proves only the URL and the
   secret: its example event is a `$pageview`, which the yard
   records as type `event`.

   The panel posts whatever globals the editor holds, and the test
   run delivers them when they match the destination's filters,
   with a real fetch while mocking is off (checked against PostHog
   at f1c6477, 23 Oct 2026:
   frontend/src/scenes/hog-functions/configuration/HogFunctionTest.tsx
   and hogFunctionTestLogic.tsx, whose test form defaults
   mock_async_functions to false; nodejs/src/cdp/cdp-api.ts,
   postFunctionInvocation and buildHogExecutorAsyncOptions;
   products/cdp/backend/api/hog_function.py, which names the
   globals "Mock global variables available during test
   invocation"; posthog.com/docs/cdp/destinations/
   customizing-destinations for the `{event}` template).
8. **Hear new issues, and file beads for them.** A subscription hears
   them in an inbox:
   ```sh
   yardr subscribe <hearer> posthog --scope all --where type=error_issue
   ```
   When the delivery carries the triggering exception's session, the
   event's fields carry the session and its replay link beside the
   project, for a subscription's `--where` to name. A makes line on the
   hook files a bead for each new issue instead, with the issue's title,
   its description and a link to it. `hook add` replaces the definition,
   so the secret file is given again, and the line names the depot,
   since the event names no repository:
   ```sh
   yardr hook add posthog --source posthog --secret-file ~/.yardr/posthog.secret --makes "action=created event=error_issue -> task:new@<depot>"
   # hook posthog saved: POST /hooks/posthog (posthog)
   # its events make a bead for a thread none tracks:
   #   action=created event=error_issue -> task:new@<depot>
   ```

On EU Cloud the calls go to `https://eu.posthog.com` instead of
`https://us.posthog.com`: the pack declares both bases, and the yard
picks one by name, in step 3 (`yardr pack apply posthog --source-base
posthog=eu`, which a re-apply keeps) or any time after (`yardr source
set posthog base=eu`). The hook needs no such setting: the issue's and
the replay's URLs are built from the `project.url` the delivery carries,
on whichever host sent it.
