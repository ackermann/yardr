# Setting up a generic webhook

The pack adds one source, `webhook`, for any service that can POST to a URL
or hand you a URL to POST to. Zapier, Make and n8n are the three it was
written for, and the steps below name each. The two directions are
separate, and you can set up either without the other.

- **Hear.** The service POSTs to a hook path of the yard with a shared
  secret in a header. The POST becomes an event, and a subscription
  delivers it (steps 2 to 4).
- **Send.** An agent POSTs a JSON object to a URL the service gave you, with
  `yardr call webhook send` (steps 5 to 7). That URL is the credential. These
  services make its path hard to guess, so anyone who holds it can start
  what it starts. The yard keeps it the way it keeps a key.

1. **Apply the pack.** As the operator:
   ```sh
   yardr pack apply webhook
   # source webhook is declared: 1 call under the URL in WEBHOOK_URL (yardr source show webhook)
   ```
   The commands that read a body and print one are written in `jq`, so the
   machine the yard runs on needs it.

## Hearing a service

2. **A hook and its secret.** The secret is yours to make. The service keeps
   whatever you give it, and the yard keeps the same value in the file the
   hook names, so put it in both places. Make one hook for each Zap,
   scenario or workflow that posts, each with a secret of its own, so you
   can turn one off without touching the rest. A new file takes its mode
   from the shell's umask, so narrow that first. Each command below is one
   thing to type, in a block of its own:
   ```sh
   umask 077
   ```
   ```sh
   openssl rand -base64 32 | tr '+/' '-_' | tr -d '=' > ~/.yardr/webhook.secret
   ```
   ```sh
   chmod 600 ~/.yardr/webhook.secret
   ```
   ```sh
   yardr hook add zapier --source webhook --secret-file ~/.yardr/webhook.secret
   # hook zapier saved: POST /hooks/zapier (webhook)
   ```
   The path ends in the hook's name, here `zapier`. Name each hook for its
   sender and give each its own secret file.
3. **The sender.** All three post the same request: a POST to
   `https://<public host>/hooks/<name>` with a JSON body and the header
   `X-Yardr-Secret` set to the secret from step 2.
   - **Zapier.** Add an action, *Webhooks by Zapier*, with the event *POST*.
     Set *URL* to the hook's address and *Payload Type* to *JSON*. Under
     *Data*, add a key and a value for each field you want to send. Under
     *Headers*, put `X-Yardr-Secret` in the left field and the secret in
     the right.
   - **Make.** Add the *HTTP* app's *Make a request* module. Set *URL* to the
     hook's address and *Method* to *POST*. Under *Headers*, add
     `X-Yardr-Secret` with the secret as its value. Set *Body content type* to
     *application/JSON* and *Body input method* to *JSON string*, then put
     the JSON object in *Body content*. This module sends only to `https://`
     addresses, so the yard's public address has to be one.
   - **n8n.** Add an *HTTP Request* node. Set *Method* to *POST* and *URL* to
     the hook's address. Turn on *Send Headers*, choose *Using Fields Below*
     under *Specify Headers*, and add the name `X-Yardr-Secret` with the
     secret as its value. To keep the secret out of the workflow, use a
     *Header Auth* credential with the same name and value instead. Turn on
     *Send Body*, set *Body Content Type* to *JSON*, and fill in *Specify
     Body*.

   Any other service that can set a header on a POST works the same way.
4. **What the yard hears.** A JSON object body can set `type`, `action`,
   `text`, `url` and `actor`. A `title` stands in where there is no `text`.
   The `url` is kept when it starts with `http://` or `https://`, and the
   `actor` is an object with an `id` and a `name`, or a bare name. What the
   body leaves out is filled in: the type is `event`, and the text is the
   body itself, compact and with its keys in alphabetical order. A body that
   is not an object, or not JSON at all, is heard as its own words. The
   event keeps the whole body. Try the hook before you set up the sender:
   ```sh
   curl -sS -X POST "https://<public host>/hooks/zapier" -H "Content-Type: application/json" -H "X-Yardr-Secret: $(cat ~/.yardr/webhook.secret)" -d '{"type":"lead","action":"created","text":"New lead: Ada"}'
   # {"seq":42}
   ```
   Without the header, or with a wrong secret, the hook answers 401 and
   records nothing. A subscription hears the events in an inbox:
   ```sh
   yardr subscribe <hearer> webhook --scope all --where type=lead
   ```
   A body that arrives twice is heard twice. A Zap that posts the same words
   every morning is not retrying, and neither is a sample you replay while
   you build one. A sender that does retry can add a second header,
   `X-Yardr-Delivery`, with an id that is the same on every try of one event.
   The yard drops a delivery whose id it has already heard.

## Sending to a service

5. **The service's URL.** Each service shows the address to POST to in a
   different place.
   - **Zapier.** Start a Zap with the trigger *Webhooks by Zapier* and the
     event *Catch Hook*. The *Test* tab shows the webhook URL, shaped like
     `https://hooks.zapier.com/hooks/catch/<account>/<hook>/`.
   - **Make.** Start a scenario with the *Webhooks* app's *Custom webhook*
     module. Adding the module shows the address Make made for it.
   - **n8n.** Start a workflow with a *Webhook* node. Set its *HTTP Method* to
     *POST*, and leave its authentication as *None* and its path as n8n
     generated it. The yard sends no header, so the path is the only check.
     Use the *Production URL* once the workflow is published. The test URL
     answers only while the editor is listening.

   Copy the URL whole, with its trailing slash if it has one. The yard sends
   to exactly the address you give it. It refuses an address that is not
   `https://`, or that has a user, a query or a fragment in it. Plain
   `http://` is allowed to this machine only, for a stand-in.
6. **Give the yard the URL.** Nothing is written by hand. The command asks
   for the URL and shows nothing as you type or paste it, and the yard's
   server writes it to a file only you can read,
   `~/.yardr/sources/webhook.env`.
   ```sh
   yardr source key webhook           # WEBHOOK_URL, the service's URL: the operator's to give
   ```
   A crew member can ask for it instead. You answer on its card in
   footplate or in `yardr chat` (`ctrl+o`), where the field shows dots:
   ```sh
   yardr bead ask <bead> "the webhook URL of the Zap" --secret webhook --context "Webhooks by Zapier, Catch Hook, the Test tab."
   ```
   The URL goes nowhere else. It is not in mail, chat, a bead or a pack, and
   no error, event or command of the yard repeats it. A yard has one URL,
   because the pack declares one source and one variable. A second URL is a
   second source. Copy the pack's directory from a yardr checkout, rename the
   source and the variable in the copy's `webhook.yaml`, and add it with
   `yardr source add <copy>/webhook.yaml`, which copies the commands in the
   `webhook/` directory beside it.
7. **Grant the call and test it.** An update call is granted by its name,
   and granting is the operator's:
   ```sh
   yardr source grant webhook send --group builders
   ```
   The source has no check call, because a hook has no request that changes
   nothing. The first send is the test, so get the Zap, scenario or workflow
   ready to receive it first. In a session of a granted group:
   ```sh
   yardr call webhook send data='{"text":"hello from the yard"}'
   ```
   `data` is one JSON object, 64 KB at most, sent exactly as written. A
   `2xx` from the service means it took the call. If the answer is JSON, it
   is printed, and Zapier's is `{"status":"success",…}`. If it is not, it is
   dropped. Make answers the word `Accepted` until you add a *Webhook
   response* module, and the call still succeeds. Each caller can make 10
   updates an hour to the source, and `yardr source set webhook
   updates_per_hour=<n>` changes that. The next call is refused until the
   hour is up.
