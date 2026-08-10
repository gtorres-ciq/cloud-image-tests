# citrun

`citrun` (`cmd/citrun`) is a single Go orchestrator that runs the full
cloud-image-tests matrix against GCE: resource-aware scheduling, per-region
budgets with stockout/quota cooldowns, per-job artifacts, explicit
state/resume, and a real report. It is a host-side CLI — it shells out to
`docker run` once per job; it never runs inside a container itself.

**Run it from the repo root.** The default run directory (`runs/<ts>/`) and
the `cleanup` subcommand's `go run ./cmd/cleanup` invocation are both
relative paths; nothing enforces cwd today.

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
binary that does resource-aware scheduling for the entire 2,030-job matrix
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
    junit.xml         test manager's JUnit output (classification reads this first, job.log as fallback)
```

## Subcommands

### `run` — start a new run

```
go run ./cmd/citrun run
```

Loads `citrun.yaml`, expands the full matrix, creates `runs/<ts>/`, sweeps
leaked resources (see `cleanup` below — `run` does this automatically,
before and after, unless you pass `-cleanup=false`), then schedules and
executes every job. Prints a ticker line every 30s and the final report on
completion. Exit code: `0` if every job ended `passed` or
`skipped_quarantine`, `1` if any job ended `failed_real`/`failed_infra`, `2`
on a setup error (bad config, bad filters, I/O failure).

### `run -dry-run` — see the plan without running anything

```
go run ./cmd/citrun run -dry-run
```

Expands the matrix and prints per-config job counts (with quarantine
counts), then exits — no run directory is created, no sweep runs. Use this
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

### `cleanup` — sweep leaked cloud resources

```
go run ./cmd/citrun cleanup
go run ./cmd/citrun cleanup -older-than 4h
```

**This deletes real cloud resources. It is not a dry-run.** `cleanup` is
the same sweep `run` already does automatically before and after every run
(so you normally never need to call it directly) — it exists standalone for
running between runs, or after an orchestrator crash skipped the
post-run sweep. It reads `-config` (default `citrun.yaml`, fully validated —
the same `LoadConfig` every other subcommand uses) to learn which GCP
project to sweep, then deletes any instance/disk/load-balancer/network
resource older than `-older-than` (default `2h`) across all 7 test regions
(`europe-west1`, `europe-west4`, `asia-southeast1`, `us-central1`,
`us-east1`, `us-east4`, `us-west1`) — it always passes `-no-dry-run` to
`cmd/cleanup`. Unlike `run`'s two internal pre/post-run sweeps (which only
WARN-log a sweep failure and carry on, since a cleanup problem shouldn't
abort a test run), the standalone `cleanup` subcommand exits `2` if the
config fails to load or the sweep itself fails (bad credentials, wrong cwd,
`cmd/cleanup` compile error, etc.) — check its exit code if you're scripting
this. Nothing currently running for less than `-older-than` is
touched (see `cleanerupper.AgePolicy`), but there is no per-run scoping: it
sweeps the whole project, not just resources tagged with your run ID.

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

**Any of these changes will change `ExpandJobs`' output — and
`cmd/citrun/golden_test.go`'s `TestGoldenParity` exists specifically to
catch that.** It checks the expanded matrix against golden cell lists in
`tools/testdata/cells-*.txt` plus hardcoded totals (2,030 jobs, 33
quarantined, etc.) drawn from the current matrix. After a deliberate matrix
change, regenerate the golden files (`tools/cell-parity.sh`, per the Phase 0
plan) and update the hardcoded counts in the test — a failure here is the
test doing its job, not a bug.

## Known v1 limitations

- **No passthrough of extra manager flags.** `DockerExecutor` builds a
  fixed set of test-manager flags per job (`-project`, `-filter`, `-zones`,
  `-images`, `-parallel_count`, `-out_path`, `-timeout`, plus the
  shape/family selector) entirely from `Job`/`Config` data. There is no
  citrun flag to inject an arbitrary extra argument into the manager
  invocation inside the container.
- **`cleanup` sweeps the whole project**, not just the current run's
  resources (see above) — treat `-older-than` as your safety margin against
  in-flight VMs, not a per-run filter.
