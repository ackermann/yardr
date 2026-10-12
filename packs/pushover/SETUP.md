# Setting up Pushover

The pack wires Pushover into the yard as the declared source `pushover`:
one push to one person. What lands in a user's inbox then reaches their
phone. Docs has the longer telling: `docs/pushover.md`.

1. **Two keys from Pushover.** On pushover.net, logged in, with its app
   on the phone: **Your User Key**, on the first page after login,
   30 letters and digits — it is where *you* are reached; and **an
   application token**, "Create an Application/API Token"
   (pushover.net/apps/build), with any name, say `yardr` — it is the
   *yard's*. Pushover's free allowance is 10,000 messages a month for
   an account; yardr sends one user at most 20 an hour.
2. **Each key in a file of its own.** Two files that only you can read,
   and nowhere else: not into mail, chat, a bead or a pack. The steps
   use `mckean` as the user; put the told person's name there.
   ```sh
   mkdir -p ~/.yardr/sources ~/.yardr/users
   printf 'PUSHOVER_TOKEN=%s\n' 'PASTE-THE-APPLICATION-TOKEN' > ~/.yardr/sources/pushover.env
   printf 'PUSHOVER_USER=%s\n' 'PASTE-YOUR-USER-KEY' > ~/.yardr/users/mckean.env
   chmod 600 ~/.yardr/sources/pushover.env ~/.yardr/users/mckean.env
   ```
   A file that others can read is refused when it is set, with the
   `chmod` that fixes it.
3. **Apply the pack.** As the operator, with the user told through it:
   ```sh
   yardr pack apply pushover --source-env pushover=~/.yardr/sources/pushover.env --notify mckean=pushover,~/.yardr/users/mckean.env
   # source pushover is declared: 1 call under https://api.pushover.net/1 (yardr source show pushover)
   # user mckean: told outside the yard: through pushover (PUSHOVER_USER is set in …/mckean.env)
   ```
   The same two settings on their own are `yardr source set pushover
   env_file=…` and `yardr user set mckean notify=pushover env_file=…`.
4. **One test message.**
   ```sh
   yardr source check pushover --user mckean
   ```
   The phone buzzes with "yardr: a test", and the command prints
   `pushover: told mckean: 200 in …ms`. This is one real message
   through Pushover, sent by the command itself. Anything but 200
   fails the command with the status: 400 is a token or a user key it
   does not take, so look at the two files again.

Mail to the user and their own timers are told from now on. `yardr user
list` shows who is told through what; `yardr user set mckean notify=`
ends it, and drops what was still waiting.
