# OpenTelemetry

yardr can send what happens in the yard to an OpenTelemetry collector, and
it gives every session a trace context so that an agent's own telemetry can
be found by the bead it worked on.

There are two parts, and neither needs the other:

- [`yardr otel export`](#yardr-otel-export) sends the yard's events as
  traces and logs. It is a command you run; nothing is exported until you do.
- [The trace context](#trace-context-for-the-work) is two environment
  variables every agent session and exec command gets, always.

Metrics are not built yet (*planned*). yardr does not use the OpenTelemetry
SDK: the exporter writes OTLP/HTTP JSON itself.

## `yardr otel export`

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 yardr otel export
```

The command subscribes to the event log as `yardr watch` and the web view
do, and posts to the collector as events are committed. It only reads the
yard: it is no part of the store's write path or of the daemon, and a yard
runs the same with or without it. Run it in a pane of its own or under
whatever keeps your services up; stop it with Ctrl-C.

### Where it sends

| variable | flag | meaning |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `--endpoint` | the collector's OTLP/HTTP address; spans go to `<endpoint>/v1/traces`, logs to `<endpoint>/v1/logs` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | `--traces-endpoint` | a full URL for spans, used as it is |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | `--logs-endpoint` | a full URL for logs, used as it is |
| `OTEL_EXPORTER_OTLP_HEADERS` | `--header k=v` (repeatable) | request headers, `key=value` separated by commas, values percent-encoded; a flag wins over the variable for the same key |

A flag wins over its variable. With no endpoint at all the command prints
that there is no collector and exits 0 without starting anything. With only
one of the two signal endpoints, only that signal is sent.

The encoding is always OTLP/HTTP JSON (`Content-Type: application/json`).
`OTEL_EXPORTER_OTLP_PROTOCOL` is not read; gRPC and protobuf are not spoken.
A collector's OTLP/HTTP receiver (port 4318 by convention) takes JSON.

### What it remembers

The exporter keeps its place in `<home>/otel-export.json` (`--state` names
another file): the seq of the last event it sent, and the spans that are
still open. On the next run it continues from there.

- A first run, with no state file, sends every event the yard still has.
  `--after <seq>` starts a first run later. Remove the file to start over.
- `--once` sends what there is and stops, for a cron job or a look.
- A collector that is down, or answers 429, 502, 503 or 504, is waited for:
  the request is repeated, first after a second, then at longer intervals up
  to 30 seconds (a `Retry-After` is followed, up to the same 30 seconds).
  Nothing else is read meanwhile, so nothing is skipped.
- Any other refusal (401, 404, 400) ends the command with the collector's
  answer. That signal's cursor does not move, so it is retried after you fix
  the endpoint or header. If the traces request succeeded before the logs
  request was refused, its cursor has moved and those spans are not sent
  again; the log records are retried on the next run.
- Stopped and started again, it sends no span and no log record twice. The
  one exception is a process killed between a request the collector took and
  the write of the state file that follows it: that one request is repeated.
- A second exporter on the same state file is refused.
- Events are pruned by age ([configuration.md](configuration.md#retention-keeping-yarddb-bounded)).
  An exporter that was not running for longer than the yard keeps events
  has missed what was pruned.

### Traces

A bead is a trace. Its spans:

| span | name | starts | ends | parent |
|---|---|---|---|---|
| a stay in a stage | `stage <stage>` | the bead enters the stage (`created`, `advanced`, `reopened`) | it leaves (`advanced`) or closes (`closed`) | none |
| a session | `session <group>` | `claimed` | the session ends: the bead moves or closes, is held or released, or the session died, failed or was aborted | the stay it ran in |

A span is sent when it ends, so a bead that sits in `review` shows there
only once it has left. A bead that goes new, review, new, review, approved,
merged has six stage spans in one trace; entering a terminal stage closes
the bead in the same step, so its span has no length.

Stage spans have no parent: there is no span for the bead as a whole.

Attributes:

| attribute | on | value |
|---|---|---|
| `yardr.bead` | both | the bead's handle |
| `yardr.rig` | both | its rig, when it has one |
| `yardr.stage` | both | the stage |
| `yardr.outcome` | both | how it ended. Stage: the outcome reported (`done`, `changes`, ...), `advanced` for a move with no outcome, `closed`. Session: the same, or `held`, `released`, `died`, `failed`, `aborted` |
| `yardr.sessions` | stage | how many sessions were started in the stay |
| `yardr.group` | session | the group that worked it |
| `yardr.session` | session | the session's id |
| `yardr.attempt` | session | 1 for the first session of the stay, 2 for the next |
| `yardr.runner` | session | `herdr`, `exec`, `manual` |
| `yardr.harness` | session, herdr only | the group's `kind` (`claude`, `codex`) |
| `yardr.model` | session, herdr only | the bead's `metadata.model`, else the group's `model`, when either is set |

A session that died or failed has status Error.

The rig, runner, harness and model are not in the events; the exporter asks
the yard for them when the span ends. For a bead, session or group that is
gone by then (an old log replayed after a prune) the span lacks them.

The resource is `service.name=yardr`, with `service.version` when the build
has one.

#### Ids

Every id is derived from what it names, so they are the same on every run
and every machine, and the runner that starts a session and the exporter
that later sends its span agree without talking:

| id | is the first bytes of sha256 of |
|---|---|
| trace id (16 bytes) | `yardr bead <bead>` |
| session span id (8 bytes) | `yardr session <session id>` |
| stage span id (8 bytes) | `yardr stay <bead> <seq of the event that began the stay>` |

So the trace of a bead can be found without looking anything up:

```sh
printf 'yardr bead yardr-n63q' | shasum -a 256 | cut -c1-32
```

The bead's handle is used, not its UUID: it is what events carry. Two yards
with a bead of the same handle that send to one backend share a trace id.

#### Epics

Each stage span of an epic's child carries a link to the epic's trace, with
the attribute `yardr.epic`. A link needs a span id, and there is no span for
a bead as a whole, so the link's span id is a derived one (sha256 of
`yardr bead span <epic>`) that no span has: a backend opens the epic's trace
from it, but finds no span to focus.

### Logs

Every event is one log record, bead or no bead:

| field | value |
|---|---|
| body | the event's kind (`advanced`, `claimed`, `session_died`, ...) |
| time | when the event was committed |
| `traceId` | the trace of the event's bead, when it has one |
| `yardr.kind` | the kind again, as an attribute to filter by |
| `yardr.actor` | who did it |
| `yardr.seq` | the event's sequence number |
| `yardr.bead` | the bead's handle, when it has one |
| `yardr.data.<key>` | the event's data, for these keys only: `from`, `to`, `stage`, `outcome`, `group`, `session`, `resident`, `flow`, `hook`, `kind`, `runner` |

Severity is INFO for all of them.

### Ids only

By default nothing a person or an agent wrote leaves the machine: no title,
body, note, reason, mail text or prompt. What is sent are handles and names:
beads, rigs, stages, groups, sessions, residents, flows, hooks and actors.
An actor is often a person's login name.

`--include-text` adds:

- `yardr.title`, the bead's title, on every span;
- on a log record, every plain value (string, number, true/false) of the
  event's data as `yardr.data.<key>`: titles, close and release reasons,
  why a session failed, mail text, the value of a changed setting;
- the reason as the status message of a failed session's span.

Bodies, notes and prompts are not in the events and are not sent with the
flag either.

## Trace context for the work

Every agent session (a herdr group) and every exec command gets, in its
environment:

| variable | value |
|---|---|
| `TRACEPARENT` | `00-<the bead's trace id>-<the session's span id>-01`, the [W3C trace context](https://www.w3.org/TR/trace-context/) of the session's span |
| `OTEL_RESOURCE_ATTRIBUTES` | `yardr.bead=<bead>,yardr.stage=<stage>,yardr.group=<group>,yardr.rig=<rig>` (no `yardr.rig` for a bead with no rig), values percent-encoded |

They are set whether or not anything collects; a program that exports no
telemetry ignores them. yardr turns no harness's telemetry on.

`OTEL_RESOURCE_ATTRIBUTES` keeps what was there: the attributes the daemon
itself was started with come first, then those of the group's `env_file`
(herdr groups, [groups.md](groups.md)), then the bead's. `TRACEPARENT` is
always the session's; an `env_file` cannot replace it.

Residents (a mayor) work no single bead and get neither.

A program that reads `TRACEPARENT` sends its spans as children of the
session's span, which `yardr otel export` sends when the session ends. One
that does not still marks what it sends with `yardr.bead`, so its data can
be filtered by bead.

### Claude Code

What follows was read in Claude Code's documentation
([Monitoring](https://code.claude.com/docs/en/monitoring-usage), fetched
2026-10-04). It was not run: no Claude Code session was started with these
settings against a collector.

```sh
CLAUDE_CODE_ENABLE_TELEMETRY=1
OTEL_METRICS_EXPORTER=otlp
OTEL_LOGS_EXPORTER=otlp
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf     # no default: it must be set
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
# traces are a beta:
CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1
OTEL_TRACES_EXPORTER=otlp
```

Put them where the agent's pane gets them: the group's `env_file` (mode
600, see [groups.md](groups.md)), or the environment herdr runs in. They
also work in `~/.claude/settings.json` under `env` and in managed settings;
Claude Code ignores them in a repository's `.claude/settings.json`. Do not
set `OTEL_RESOURCE_ATTRIBUTES` in a settings file: a value there replaces
the one in the environment, and with it the bead's attributes.

What to expect, by that documentation:

- `OTEL_RESOURCE_ATTRIBUTES` is honoured: the `yardr.*` attributes are on
  every metric and event and in the resource. So tokens, cost and tool calls
  can be summed by `yardr.bead` and `yardr.stage`. Its rules for the value
  (no spaces, percent-encoding) are the ones yardr writes it by.
- `TRACEPARENT` is read only by non-interactive sessions (`claude -p`, the
  Agent SDK). An interactive session ignores it, and the agents a herdr
  group starts are interactive: their spans form traces of their own, not
  children of the session's span. An exec group whose command runs
  `claude -p` does get its spans under the session's span.

### Codex

Read in Codex's documentation
([advanced configuration](https://developers.openai.com/codex/config-advanced),
[configuration reference](https://developers.openai.com/codex/config-reference),
fetched 2026-10-04), not run. Codex is configured in `~/.codex/config.toml`,
not by environment variables:

```toml
[otel]
environment = "dev"
log_user_prompt = false
exporter         = { otlp-http = { endpoint = "http://localhost:4318/v1/logs",    protocol = "binary" } }
trace_exporter   = { otlp-http = { endpoint = "http://localhost:4318/v1/traces",  protocol = "binary" } }
metrics_exporter = { otlp-http = { endpoint = "http://localhost:4318/v1/metrics", protocol = "binary" } }
```

The keys and the shape of `otlp-http` (`endpoint`, `protocol` `binary` or
`json`, `headers`) are from the reference. Its only example endpoint is the
one for logs; the `/v1/traces` and `/v1/metrics` paths above follow the OTLP
convention and are a guess.

Codex's documentation says nothing about `TRACEPARENT` or
`OTEL_RESOURCE_ATTRIBUTES`. Its source (github.com/openai/codex at
`de3721a`, read, not run) reads `TRACEPARENT` for `codex exec` and the app
server, and nowhere for the interactive terminal UI that a herdr group
starts; it never names `OTEL_RESOURCE_ATTRIBUTES`, which may still be picked
up by the OpenTelemetry library it uses. Whether a Codex agent's telemetry
carries `yardr.bead` is therefore not known. Check it in your collector
before relying on it.

## A worked example: Grafana

This example was written from the documentation of Grafana Alloy, Tempo and
Loki. **It was not run against a real Grafana**: the exporter is tested
against an HTTP test server and was tried against a plain HTTP listener, not
against Alloy, Tempo or Loki.

Grafana Alloy receives OTLP on port 4318 and hands spans to Tempo and logs
to Loki. `config.alloy`:

```alloy
otelcol.receiver.otlp "yardr" {
  http {
    endpoint = "127.0.0.1:4318"
  }
  output {
    traces = [otelcol.exporter.otlp.tempo.input]
    logs   = [otelcol.exporter.otlphttp.loki.input]
  }
}

otelcol.exporter.otlp "tempo" {
  client {
    endpoint = "tempo:4317"
    tls {
      insecure = true
    }
  }
}

otelcol.exporter.otlphttp "loki" {
  client {
    endpoint = "http://loki:3100/otlp"
  }
}
```

Then:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318 yardr otel export
```

Without Alloy, Tempo and Loki each take OTLP/HTTP themselves, at two
addresses, so name them apart:

```sh
yardr otel export \
  --traces-endpoint http://tempo:4318/v1/traces \
  --logs-endpoint http://loki:3100/otlp/v1/logs
```

For Grafana Cloud, `--endpoint` is the stack's OTLP gateway and the
credentials go in a header:
`--header 'Authorization=Basic <base64 of instance-id:token>'`.

In Grafana:

- Tempo, a bead's trace: search by trace id (the `shasum` line
  [above](#ids)), or with TraceQL `{ span.yardr.bead = "yardr-n63q" }`.
  Sessions that died: `{ span.yardr.outcome = "died" }`. Time in review:
  `{ name = "stage review" }` and the span's duration.
- Loki, a bead's events: `{service_name="yardr"} | yardr_bead="yardr-n63q"`
  (Loki writes the dots of an attribute name as underscores). Each record
  carries its trace id, so a log line opens the bead's trace when the Loki
  data source has a derived field or correlation to Tempo.
- With a harness's telemetry on, the same Alloy receives it; filter it by
  the resource attribute `yardr.bead`.
