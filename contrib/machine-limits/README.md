# contrib/machine-limits

The `machine` source: load, swap and disk of this machine as limits
windows, for route conditions that name a source
(`muse-builders@machine.load<80 else wait`).

```sh
yardr source add contrib/machine-limits/machine.yaml   # from main
yardr source check machine
yardr limits                                           # the three windows beside the providers
yardr route add --stage new --group 'muse-builders@machine.load<80' --else wait
```

Add it from `main`, not a checkout, so a branch cannot change what the
yard runs. `yardr source add` copies `machine/limits` into the yard, as
for every command a file names.

The command answers three windows as the limits JSON does, each with the
target the declaration sets (`--target load=80`, `swap=85`, `disk=90`;
the script's defaults are the same):

- `load`: the 1-minute load average over the core count, as a percent (10
  cores at load 8 = 80). It may read past 100.
- `swap`: used over total swap, or 0 where the machine has none.
- `disk`: used over the volume that holds the yard's home, which the core
  runs the command from.

None of them resets, so none carries `resets_at`, and a `quota_wait` on
one names no reset. The command reads `uptime`, `sysctl` and `df` on
macOS, `/proc/loadavg`, `/proc/meminfo` and `df` on Linux, and refuses
anywhere else. What it cannot read is no answer, as for a provider: a
route's condition then holds, and the `claimed` event says the limits
were not known.

`go test ./contrib/machine-limits/` runs the script: its answer on this
machine, its targets, and every refusal.
