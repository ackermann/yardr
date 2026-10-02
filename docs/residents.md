# Residents: always-on sessions (a mayor)

A resident is an agent session that is not tied to a bead. The daemon keeps it
running in its directory, nudges it when its inbox has something new and it is
idle, and rotates it through handoffs instead of letting its context degrade.

```sh
yardr resident add                     # interactive: agent, model, permissions, dir, inbox, rotation
yardr resident add mayor --set kind=claude --set 'args=["--permission-mode","auto"]' \
  --set prompt=roles/mayor.md --dir ~/projects --max-age 6h
yardr mail mayor "Deploys are frozen until Friday"
yardr resident show mayor              # state, unread inbox, recent journal
```

- **Inbox:** mail, plus what its subscriptions deliver. Read with `yardr
  inbox`, mark handled with `yardr inbox --ack`; the cursor is stored, so
  nothing is lost or repeated across restarts.
- **Subscriptions:** `--on` kinds (default: things that went wrong or need a
  decision: `gave_up`, `session_blocked`, `unrouted`, …), webhook deliveries
  (`yardr subscribe <name> linear --scope involved`), and yardr's own bead
  events (`yardr subscribe <name> yardr --done`, `yardr bead watch <id>`).
  Each delivery records why it reached the resident, and `yardr inbox` shows
  it. See [subscriptions.md](subscriptions.md).
- **Journal:** one bead per resident (`<name>-journal`). `yardr resident journal`
  keeps decisions (≤4 KB); `yardr resident handoff` ends the session (≤16 KB) and
  the next session starts from it. At `--max-age` the resident is asked for a
  handoff when idle; after `--handoff-grace` yardr restarts it regardless.
- **Prime:** a new session reads `yardr prime --resident <name>`: its role, the
  latest handoff, journal entries since, its inbox and its memories' titles,
  bounded in size. If the last session ended without a handoff, it gets a
  summary of what happened since instead.
- **Memories:** `yardr remember "title" -b … [--expires 7d] [--scope all]` and
  `yardr recall [query]`: lasting knowledge as beads, listed by title in primes,
  retired automatically when they expire.

Agents are told to run yardr by absolute path: a shell may find an older yardr
first on its PATH, and the prime warns about it.

## Outbound credentials

What a resident uses to act outside the yard (the mayor's Linear developer
token or OAuth app, a GitHub token) goes in an env file of its own, never in
the store: BDP serves the store, backups copy it and every builder can read
it. Inbound is separate: webhooks arrive through one shared hook and its
secret file (`yardr hook add linear --source linear --secret-file …`); outbound is per
resident.

```sh
mkdir -p ~/.config/yardr/residents
cat > ~/.config/yardr/residents/mayor.env <<'END'
# Linear, as the mayor
LINEAR_API_KEY=lin_api_…
END
chmod 600 ~/.config/yardr/residents/mayor.env
yardr resident add mayor --set env_file=~/.config/yardr/residents/mayor.env …
yardr resident show mayor     # env_file …/mayor.env (LINEAR_API_KEY)
```

- Lines are `KEY=VALUE`; blank lines and `#` comments are skipped. Nothing is
  expanded: the value is the rest of the line as written. `~` in the
  `env_file` path is the daemon user's home.
- Each entry joins the pane environment next to `YARDR_RESIDENT`,
  `YARDR_ACTOR` and `YARDR_HOME`. A key yardr sets itself is ignored, so a
  credentials file cannot change who the agent is.
- The file must be a regular file that only its owner can read or write
  (mode `0600` or stricter). Missing, not a regular file or open to others,
  the resident does not start: the start fails with the reason (backed off
  as any failed start) and a `resident_blocked` event in its journal says
  so, once per reason.
- The file is read at each start, so a new token takes effect on the next
  restart (`yardr resident restart mayor`). Values are never logged, put on
  the session's handle or shown; `yardr resident show` lists the key names.
