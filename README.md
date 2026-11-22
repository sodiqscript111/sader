# damon-wss-exporter

A node agent that measures how much memory each container actually touches,
not only how much it holds, and exports that to Prometheus next to the
container's request and limit.

**Status:** milestone 3 in progress. `pkg/damonfs` and `cmd/damon-probe` exist
and pass their tests against an in-memory fake kernel. Nothing has run on a
real kernel yet.

## Layout

| Path | What it is |
|---|---|
| `pkg/damonfs` | Typed wrapper over `/sys/kernel/mm/damon/admin`: slots, configuration, start/stop/commit, tried regions, stats, feature detection |
| `pkg/damonfs/damonfstest` | In-memory fake of the sysfs tree that behaves like the kernel on writes; lets everything above it be tested on any OS |
| `cmd/damon-probe` | Smoke test: detects DAMON, then prints one PID's working set every second |
| `pkg/discovery`, `pkg/sampler`, `cmd/agent`, `deploy/`, `hack/` | Not started |

## Try it on a Linux VM

Ubuntu 24.04 or later, kernel 6.8 or later:

```sh
zgrep -E 'CONFIG_DAMON(_VADDR|_SYSFS)?=' /boot/config-$(uname -r)
go build -o bin/damon-probe ./cmd/damon-probe
sudo ./bin/damon-probe                      # detect support only
sudo ./bin/damon-probe -pid $(pgrep -n myapp) -count 30
```

Exit code 2 means the kernel has no DAMON sysfs interface.

Tests run anywhere: `go test ./...`

## Design notes found while building `damonfs`

These change the plan, so they're recorded here. Each still needs checking on a
real kernel (see the last section).

1. **Reserve a fixed pool of kdamonds at startup.** Writing
   `kdamonds/nr_kdamonds` deletes and recreates every kdamond directory,
   discarding their configuration, and the kernel refuses it with `EBUSY` while
   any kdamond runs. The agent therefore can't add a kdamond when a new
   container appears. It should call `Reserve(maxContainers)` once, then
   assign containers to free slots. The pool size also gives you the overhead
   cap from the risk list.
2. **PID churn uses `commit`, not restarts.** `UpdateTargets` rewrites the
   target list and writes `commit` to a running kdamond, so forks and restarts
   don't lose monitoring history.
3. **A vaddr kdamond stops by itself when all its target processes exit.** The
   agent must poll `State()` and recycle the slot.
4. **Read the working set from a single snapshot, using region age.** A region's
   `age` counts aggregation intervals since its access count last changed, so
   for an idle region `age × aggr` approximates how long it has been idle.
   WSS over window W = bytes accessed now + idle bytes with `age × aggr ≤ W`.
   This gives 1m, 5m and 1h windows without keeping an hour of snapshots
   (`WorkingSetBytes`). Keep the ring buffer for p50/p95 over time.
5. **Prefer `total_bytes` to listing regions.** Listing regions costs 4 file
   reads per region: up to 4,000 reads per container per sample with
   max_regions=1000. A cheaper option is one `stat` scheme per question:
   `HotPattern()`, `RecentlyIdlePattern(1m)`, `RecentlyIdlePattern(1h)`. Then
   write `update_schemes_tried_bytes` and read one `total_bytes` per scheme.
   `TestPatternSchemesMatchRegionMath` checks that both methods agree.
6. **"Cold bytes" can't be `monitored − hot`.** vaddr monitors three big
   address ranges that include unmapped holes, so the sum of region sizes isn't
   RSS. Compute `damon_cold_bytes` as cgroup `memory.current` (or RSS) minus
   WSS.
7. **One kdamond per container.** Contexts are capped at 1 per kdamond, and
   tried regions don't record which target they came from. One kdamond with
   several targets therefore can't attribute memory to containers.

## Verify on a real kernel (milestone 2–3)

- [ ] `nr_kdamonds` returns `EBUSY` while one runs, and wipes stopped slots' config
- [ ] `commit` with a changed PID list keeps monitoring without a restart
- [ ] The kdamond goes `off` when its only target exits
- [ ] `update_schemes_tried_regions` blocks until the snapshot is ready
- [ ] Idle regions' `age` grows as expected, and splits or merges don't reset it too often for note 4 to work
- [ ] `total_bytes` and `apply_interval_us` exist on your kernel (`damon-probe` prints the features)
- [ ] CPU per kdamond thread (`damon-probe` prints its PID; check with `top -H -p`)
