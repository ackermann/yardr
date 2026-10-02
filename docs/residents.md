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
