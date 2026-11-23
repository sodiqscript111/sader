# sader

A node agent that measures how much memory each container actually touches,
not only how much it holds, and exports that to Prometheus next to the
container's request and limit.

**Status:** early. `pkg/damonfs`, the Way 2 engine and `cmd/damon-probe` pass
their tests against an in-memory fake kernel. Nothing has run on a real kernel
yet.

## Two ways to measure

- **Way 1, by process** (`vaddr`): watch one program's own memory by PID. Works
  on older kernels (5.15+) and on any program. Used by the script.
- **Way 2, by container** (`paddr` + memcg filters): one watcher sees all of the
  server's memory and counts the touched bytes that belong to each container.
  It works for gVisor and other sandboxes too. Needs a kernel with DAMOS
  filter-passed stats (around 6.14+, still to be confirmed on a real kernel).
  Used by the Kubernetes agent, which falls back to Way 1 on older kernels.

## Layout

| Path | What it is |
|---|---|
| `pkg/damonfs` | Typed wrapper over `/sys/kernel/mm/damon/admin`: slots, configuration, start/stop/commit, filters (both `core_filters`/`ops_filters` and legacy `filters` layouts), tried regions, stats, feature detection |
| `pkg/damonfs/damonfstest` | In-memory fake kernel: reacts to writes like the kernel, and models memory ownership per cgroup so filter results can be tested on any OS |
| `pkg/measure/cgroup` | Way 2 engine: one `paddr` watcher, one stat scheme per container, per-container bytes / hot bytes / 1m-5m-1h working set, optional page-level mode |
| `pkg/schedule` | Adaptive checking: containers whose numbers stay flat are checked less and less often (10s, 20s, 40s ... up to 5 min); any real change snaps back to 10s. In a simulated 10 minutes with 40 containers (1 busy), this cut file reads 8x |
| `cmd/damon-probe` | Smoke test for Way 1: detects DAMON, then prints one PID's working set every second |
| `hack/memhog` | Validation workload: fills N MiB, keeps touching M MiB |
| `hack/wsl-damon-kernel.sh` | Builds a WSL2 kernel with DAMON switched on |

Planned, in order: `pkg/discovery` (Nomad and Docker first, then Kubernetes),
`cmd/sader-agent` (Prometheus export; Nomad system job, then DaemonSet),
`pkg/measure/process` (Way 1), `cmd/sader` (script), `pkg/window`,
`cmd/sader-operator` (recommendations), `deploy/`.

## First real-kernel results

WSL2, Microsoft kernel 6.18.58.1 built with DAMON (`hack/wsl-damon-kernel.sh`),
`hack/realtest.sh`. Each memhog fills 1 GiB and keeps touching part of it.

| Test | Really touched | sader (hot, after ~10s) | Holds | sader "seen" |
|---|---|---|---|---|
| Way 1, one process | 200 MiB | ~175 MiB (-12%) | RSS 1031 MiB | n/a |
| Way 2, cgroup A | 200 MiB | ~175 MiB (-12%) | memory.current 1032 MiB | 1.01 GiB |
| Way 2, cgroup B | 100 MiB | ~97 MiB (-3%) | memory.current 1032 MiB | 1.01 GiB |

**gVisor** (`runsc` release-20261116.0, systrap platform, `hack/realtest.sh gvisor`,
memhog 1 GiB filled / 200 MiB touched):

| Test | sader | Notes |
|---|---|---|
| Sandbox cgroup holds | 1.06 GiB seen vs memory.current 1089 MiB | gVisor's memory is charged to the sandbox's cgroup |
| Way 2 on that cgroup | ~130-170 MiB hot, noisy in the first ~10s | works; less accurate than for a plain process |
| Way 1 on one systrap helper process | ~197-201 MiB hot | most accurate; the helpers share the app's address space |

`runsc do` puts the sandbox in a new cgroup (`/runsc-<id>`), not the caller's;
under containerd/Docker/Nomad the runtime passes the container's cgroup
instead. KVM platform is untested (no `/dev/kvm` in WSL).

Learned on the real kernel (the fake kernel now models both):

- A `paddr` target with no address ranges watches nothing and reports nothing.
  Way 2 now reads System RAM ranges from `/proc/iomem` and refuses to start
  without them.
- The 1m/5m/1h windows are only meaningful after the watcher has run that long;
  before that every region looks recently used. A warm-up rule is still to do.

## Known limits

- **Only a first real-kernel run so far.** Most tests still use the fake kernel
  in `damonfstest`, which encodes our reading of the kernel docs plus what the
  real runs above showed.
- **Way 2 sampling cost.** `Sample` lists every tried region for every
  container: about containers × regions × 5 small file reads per sample (40
  containers × 10,000 regions ≈ 2 million). `tried_regions/total_bytes` can't
  replace it because it sums region sizes, not each container's bytes. The
  cheaper path is the `sz_ops_filter_passed` stat (one read per scheme). Its
  exact behaviour (cumulative or not, per how many applications) isn't in the
  docs, so it will be measured on a real kernel before switching.
- **gVisor: systrap tested, KVM not.** See the results above.
- **A wrong cgroup path reads as 0.** The kernel filter matches nothing, so
  discovery must only pass paths that exist.

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
