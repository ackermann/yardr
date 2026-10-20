# Setting up Pushover

The pack wires Pushover into the yard as the declared source `pushover`:
one push to one person. What lands in a user's inbox then reaches their
phone. Docs has the longer telling: `yardr docs pushover`.

1. **Two keys from Pushover.** On pushover.net, logged in, with its app
   on the phone: **Your User Key**, on the first page after login,
   30 letters and digits — it is where *you* are reached; and **an
   application token**, "Create an Application/API Token"
   (pushover.net/apps/build), with any name, say `yardr` — it is the
   *yard's*. Pushover's free allowance is 10,000 messages a month for
   an account; yardr sends one user at most 20 an hour.
2. **Apply the pack.** As the operator, with the user told through it.
   The steps use `mckean` as the user; put the told person's name there.
   ```sh
   yardr pack apply pushover --notify mckean=pushover
   # source pushover is declared: 1 call under https://api.pushover.net/1 (yardr source show pushover)
   # user mckean: told outside the yard: through pushover, which is not set up: user mckean has no env_file: yardr user key mckean pushover gives PUSHOVER_USER
   ```
3. **Give the yard the two keys.** No file is written by hand: each
   command asks for its key and shows nothing as it is typed or pasted,
   and the yard's server writes it to a file only you can read
   (`~/.yardr/sources/pushover.env`, `~/.yardr/users/mckean.env`).
   ```sh
   yardr source key pushover           # PUSHOVER_TOKEN, the application token: the operator's to give
   yardr user key mckean pushover      # PUSHOVER_USER, your user key: yours to give, or the operator's
   # user mckean: PUSHOVER_USER is set in …/mckean.env
   ```
   Or have a crew member ask for them, and answer each on its card, in
   footplate or in `yardr chat` (`ctrl+o`), where the field shows dots:
   ```sh
   yardr bead ask <bead> "Pushover's application token" --secret pushover --context "pushover.net/apps/build"
   yardr bead ask <bead> "Your Pushover user key" --secret pushover --for-user mckean --context "The first page of pushover.net after login."
   ```
   The keys go nowhere else: not into mail, chat, a bead or a pack.
4. **One test message.**
   ```sh
   yardr source check pushover --user mckean
   ```
   The phone buzzes with "yardr: a test", and the command prints
   `pushover: told mckean: 200 in …ms`. This is one real message
   through Pushover, sent by the command itself. Anything but 200
   fails the command with the status: 400 is a token or a user key it
   does not take, so give the two keys again (step 3).

Mail to the user and their own timers are told from now on. `yardr user
list` shows who is told through what; `yardr user set mckean notify=`
ends it, and drops what was still waiting.
