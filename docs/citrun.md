# citrun

`citrun` (`cmd/citrun`) is a single Go orchestrator that runs the full
cloud-image-tests matrix against GCE: resource-aware scheduling, per-region
budgets with stockout/quota cooldowns, per-job artifacts, explicit
state/resume, and a real report. It is a host-side CLI — it shells out to
`docker run` once per job; it never runs inside a container itself.

**Run it from the repo root.** The default run directory (`runs/<ts>/`) is
relative; nothing enforces cwd today.

## What it replaces

Before citrun, the matrix (documented today in `test-matrix-totals.md`) was
driven by four hand-rolled bash drivers, run semi-manually and partly in
parallel:

- `run_tests.sh` — the main per-config matrix loop
- `run_all_tests.sh` — fan-out across the three main configs (x86/arm/x86-metal)
- `run_metal_x86_parallel.sh` — a region-aware scheduler hand-written just for metal C3
- `run_shapevalidation.sh` — a fully serial per-family loop

Between them: no shared state (a crash meant starting over or manually
diffing junit files), no budget awareness beyond the metal script's
hand-rolled region list, and a multi-day wall-clock run. `citrun` is one
binary that does resource-aware scheduling for the entire 2,179-job matrix
(including shapevalidation) with one `state.json`/`events.jsonl` per run and
one report at the end. See
`docs/superpowers/specs/2026-08-10-test-execution-design.md` for the full
design rationale; the bash scripts stay in the repo as a fallback until a
clean citrun month.

## Run-directory layout

Every run gets its own directory (default `runs/<timestamp>/`, or `-run-dir`):

```
runs/20260810-120000/
  citrun.yaml       frozen copy of the config used for this run (resume reads THIS, not the working copy)
  state.json        the single source of truth: every job's status + attempt history (atomic writes)
  events.jsonl       append-only transition log (one JSON line per launch/completion/abort)
  pause              touch this file to pause admission (see below); absence = running
  report.md          written at the end of `run`/`resume`/`rerun-failed`, or on demand by `report`
  report.csv         same data as report.md, one row per job
  jobs/<job-id>/
    job.log          combined stdout+stderr of the docker container
    junit.xml         test manager's JUnit output (classification reads this first, job.log as fallback);
                      for infra-classified jobs (stockout/quota/infra) failures are rewritten to
                      <skipped message="infra (...)"> so JUnit consumers count only real failures
```

## Subcommands

### `run` — start a new run

```
go run ./cmd/citrun run
```

Loads `citrun.yaml`, expands the full matrix, creates `runs/<ts>/`, then
schedules and executes every job. Prints a ticker line every 30s and the final report on
completion, ending with a `Total time:` line for the invocation's wall clock
(stdout only). Exit code: `0` if every job ended `passed` or
`skipped_quarantine`, `1` if any job ended `failed_real`/`failed_infra`, `2`
on a setup error (bad config, bad filters, I/O failure).

### `run -dry-run` — see the plan without running anything

```
go run ./cmd/citrun run -dry-run
```

Expands the matrix and prints per-config job counts (with quarantine
counts), then exits — no run directory is created. Use this
after any `citrun.yaml` edit to sanity-check the resulting job counts before
spending real GCE quota.

### filtered run — run a slice of the matrix

```
go run ./cmd/citrun run -dry-run -configs x86 -suites ssh
go run ./cmd/citrun run -configs x86 -shapes 'c3-*' -suites ssh,disk
```

`-configs`, `-images`, `-shapes`, `-suites` each take comma-separated
glob lists (`path.Match` syntax) and AND together. Combine with `-dry-run`
first to confirm the slice is what you expect.

### tiered run — run the GCE mandate or flag-only slice

```
go run ./cmd/citrun run -dry-run -tier 1
go run ./cmd/citrun run -tier 2
```

`-tier` selects a named entry from the `tiers:` section of `citrun.yaml`
(populated from the per-attempt analysis in
`gce-mandate-vs-flagonly-lists.md`). The default is `-tier all`, which runs
the full matrix exactly as before. Under a tier:

- only the shapes listed in that tier expand into jobs (all their configured
  images and suites run);
- each listed shape's zone sweep is **replaced** with the tier's zones — tier
  zones need not be in the shape's `zone_set` (most tier-2 zones are
  deliberately absent from it);
- the `shapevalidation` families do not run.

Use `-dry-run` first: tier entries change placement, not job counts, so the
per-config totals should match the shapes×images×suites arithmetic for the
tier's shapes.

### `resume` — continue an interrupted run

```
go run ./cmd/citrun resume runs/20260810-120000
```

Re-reads that run's frozen `citrun.yaml` and `state.json`, reconciles
`running` jobs against still-live Docker containers (attaches and
classifies if the container survived; marks `orphaned` and requeues if it
didn't), then requeues every `queued` and `failed_infra` job and continues
scheduling. **`resume` never requeues `failed_real`** — a real test failure
needs a human look, not an automatic retry; use `rerun-failed` once you've
decided it's worth retrying anyway (a flake, a since-fixed image, etc.).

### `rerun-failed` — retry real failures too

```
go run ./cmd/citrun rerun-failed runs/20260810-120000
```

Identical to `resume`, except `failed_real` jobs are requeued as well.

Both `resume` and `rerun-failed` accept `-dry-run`:

```
go run ./cmd/citrun rerun-failed -dry-run runs/20260810-120000
```

It prints the requeue plan — counts by category, then every job ID that
would requeue, plus any still-live containers that would be awaited/attached
instead — and exits without modifying `state.json` or launching anything.
`-refresh-config` is ignored in dry-run mode (it mutates state, so it is
skipped).

After editing a run's saved `citrun.yaml`, explicitly refresh its saved job
placement and CPU accounting before retrying:

```bash
go run ./cmd/citrun rerun-failed --refresh-config runs/20260908-141435/
```

Stop the original runner first. This option (also available on `resume`)
updates `Zones`, `CPUCost`, and `BudgetKey` for queued/failed jobs and
resets their stockout sweeps and stockout cooldowns,
matching IDs against the edited config's expanded matrix. Passed and
quarantined jobs and all attempt history remain unchanged. Normal resume
status transitions still apply afterward. It does not add new jobs or refresh
other job definition fields. Without the flag, saved job definitions remain unchanged.

The refresh rejects unmatched jobs, running jobs, and jobs that cannot fit
any configured regional CPU budget. Validation uses configured budgets, not
live GCP capacity. Before saving, it creates a unique
`state.json.pre-refresh-*` backup in the run directory and logs its path.
If state still contains running jobs, reconcile them using ordinary resume
before attempting a refresh; do not edit state while a runner is active.

### `status` / `status -watch` — check a run's progress

```
go run ./cmd/citrun status runs/20260810-120000
go run ./cmd/citrun status -watch runs/20260810-120000
```

Prints counts by job status, per-region running-job/CPU occupancy, any
active stockout/quota cooldowns with seconds remaining, and a rough ETA —
all derived from that run's `state.json` alone, so it's safe to run in a
second terminal alongside a live `run`/`resume`. Without `-watch` it prints
once and exits (`0`, or `2` if the run directory doesn't have a readable
`state.json`). With `-watch` it clears the screen and reprints every 5s
until you kill it (Ctrl-C) or it hits a read error.

### `report` — regenerate the report for a run

```
go run ./cmd/citrun report runs/20260810-120000
go run ./cmd/citrun report -top 30 runs/20260810-120000
```

Rebuilds `report.md`/`report.csv` from the current `state.json` and prints
the markdown to stdout — useful mid-run for a progress snapshot with more
detail than `status`, or to regenerate the report after manually editing
state. `-top N` controls how many of the slowest passed jobs are listed
(default 15).

## Stockout and quota retries

A stockout removes only the failed zone from that job's current placement
sweep. The scheduler immediately tries its other eligible zones, including
zones in the same region. Other shapes and jobs remain schedulable. Zones
that cannot fit the job's configured CPU budgets are excluded from the
sweep; temporarily busy regions are not counted as failed.

Only after every eligible zone has failed does the job wait: 2 minutes,
then 4, then 8 between sweeps. Four exhausted sweeps mark it `failed_infra`.
The failed zones, completed sweep count, and cooldown survive interruption.
Explicitly resuming a `failed_infra` job starts fresh sweeps and preserves
its attempt history. Cooldowns appear in status as `stockout|<job-id>`.

Quota failures remain regional, keyed by `quota|<budget-key>|<region>`,
with exponential cooldowns and the existing temporary budget penalty.
Failures arriving while that cooldown is active do not extend it or
increase its backoff. Quota failures retain their four-attempt limit.

When resuming an older run, legacy series-wide stockout cooldowns are
removed so eligible zones can be tried. Recent quota failures retain their
regional restriction. No manual state edits are needed. Stop the old
runner before resuming with the updated binary; use `--refresh-config` if
you also changed the run's saved zone lists.

## Pausing a run

```
touch runs/20260810-120000/pause
rm runs/20260810-120000/pause
```

While the pause file exists, the scheduler stops admitting new jobs (checked
once per admission pass); jobs already running are left alone and continue
to completion. Removing the file resumes admission on the next pass — no
restart needed.

## Stopping a run (Ctrl-C)

`run`/`resume`/`rerun-failed` all install the same two-stage signal handler:

- **First Ctrl-C (or SIGTERM): drain.** No new jobs are admitted; jobs
  already running continue and get awaited, classified, and persisted
  normally. This is the safe way to stop — nothing in flight is killed.
- **Second Ctrl-C: kill.** Every still-running job's container is killed
  immediately (`docker kill`) and its attempt is recorded `aborted` (not
  counted against any retry cap, so a later `resume` requeues it cleanly).

Either way, `resume` afterwards picks up exactly where the run left off.

## Extending the matrix: adding a shape, suite, or quarantine entry

`citrun.yaml` is the single source of truth for the matrix — `shapes:`,
`suites:`, `matrix:` (which shapes × images × suites combinations exist),
`rules:`, and `quarantine:`. For example, the existing shapes and quarantine
entries look like:

```yaml
shapes:
  c3-standard-4: {arch: x86, cpus: 4, family: C3_CPUS, zone_set: default}

suites:
  ssh: {vms: 4, timeout: 30m, est: 8m30s}

quarantine:
  - {image: rocky-linux-8-optimized-gcp, suite: imageboot, reason: "secureboot shim rejected by GCE"}
```

To add a new shape or suite: add its entry under `shapes:`/`suites:`, then
reference its name from the relevant `matrix:` entry's `shapes:`/`suites:`
list (a shape or suite that's never referenced from `matrix:` — or from
`shapevalidation.families` — is loaded but never expands into a job). A new
shape needs a `zone_set` that already has budgets for every region in it
(`Validate()` rejects it otherwise). To quarantine an image/shape/suite
combination, add a `quarantine:` entry with a `reason` — quarantined jobs
still expand and appear in `state.json`/reports (status
`skipped_quarantine`) but the scheduler never admits them.

By default, the zones in a shape's `zone_set` are placement alternatives:
one job runs in one zone, and the remaining zones are available for retry.
Set `each_zone: true` on a matrix entry when every zone is part of the test
coverage. `citrun` then creates one independently reported job per zone,
pins each job to that zone, and appends the zone to its job ID.

A `tiers:` section (`map[name][]TierEntry`, each `{shape, zones}`) is only
consulted when `run` is invoked with `-tier <name>`; with the default
`-tier all` it has no effect. A tier entry replaces the named shape's zone
sweep and excludes any shape not listed from that tier's expansion. Every
tier zone's region must have budgets, and the shape must exist — `Validate()`
enforces both. Quote numeric tier keys (`"1":`) so YAML keeps them strings.

**Any of these changes will change `ExpandJobs`' output — and
`cmd/citrun/invariants_test.go`'s `TestMatrixInvariants` exists specifically to
catch that.** It asserts the expanded matrix's job totals and safety controls
(U4 quota serialization, single-zone pinning, budget relationships, suite
gating) against the real `citrun.yaml`. After a deliberate matrix change,
update the hardcoded counts and expectations in that test — a failure there is
the test doing its job, not a bug. (The former bash-harness parity check and
its `tools/cell-parity.sh` / `tools/testdata/cells-*.txt` goldens were removed
once the citrun migration was completed.)

## Known v1 limitations

- **No passthrough of extra manager flags.** `DockerExecutor` builds a
  fixed set of test-manager flags per job (`-project`, `-filter`, `-zones`,
  `-images`, `-parallel_count`, `-out_path`, `-timeout`, plus the
  shape/family selector) entirely from `Job`/`Config` data. There is no
  citrun flag to inject an arbitrary extra argument into the manager
  invocation inside the container.
