# citrun config schema

Reference for the YAML files consumed by `citrun run -config` (source of
truth: `cmd/citrun/config.go` in gtorres-ciq/cloud-image-tests). The kernel
matrix for this repo's pipeline is `configs/citrun-kernel.yaml`.

Unknown keys are rejected (`KnownFields(true)`), so any typo here fails the
run at config-load time. Durations accept Go-style strings (`45m`, `2m30s`,
`90s`). Field names are exactly the YAML keys below; Go names in parens.

## Top-level keys

| Key | Type | Default | Notes |
|---|---|---|---|
| `project` | string | — | GCP project tests run in. **Required.** |
| `image_prefix` | string | — | Prefix for image refs (usually `projects/<proj>/global/images/family`). |
| `docker_image` | string | `cloud-image-tests` | Local container image name the test workflow uses. |
| `container_cap` | int | `80` | Max concurrently running jobs. |
| `safety_factor` | float (0,1] | `0.8` | Applied to every budget row; explicit `0` is rejected (sentinel: absent = default). |
| `budgets` | table | — | See below. **Required** (zone_sets validate against it). |
| `zone_sets` | map: name → zone list | — | Named zone pools; every zone's region must have a `budgets.regions` row. |
| `shapes` | map: name → shape | — | See below. |
| `suites` | map: name → suite | — | See below. |
| `matrix` | list of matrix rows | — | See below. |
| `rules` | list | — | Filters applied to the expanded matrix; see Rules. |
| `quarantine` | list | — | See Quarantine. |
| `tiers` | map: name → tier entry list | — | See Tiers. |
| `shapevalidation` | table | — | See Shapevalidation. |
| `retries` | table | — | Retry/backoff knobs; see Retries. |

## budgets

```yaml
budgets:
  networks: 90        # max concurrently used networks
  subnetworks: 275    # max concurrently used subnetworks
  regions:
    us-central1:      # key = region; values = budget-key -> CPU limit
      CPUS: 3000
      C3_CPUS: 1920
```

Region rows must cover every region containing a zone used by any
`zone_sets` entry (and tier/shapevalidation pinned zones). Budget keys are
conventionally the machine family (`CPUS` plus e.g. `C3_CPUS`, `T2A_CPUS`);
a shape's `family` must exist in the regions it runs in.

## shapes

```yaml
shapes:
  c3-standard-4: {arch: x86, cpus: 4, family: C3_CPUS, zone_set: kernel-default}
  c3-standard-192-metal: {arch: x86, cpus: 192, family: C3_CPUS,
                          zone_set: kernel-metal-c3, metal: true,
                          max_parallel: 2}
```

| Key | Notes |
|---|---|
| `arch` | `x86` or `arm64`. |
| `cpus` | Must be > 0; CPU cost charged against region budgets. |
| `family` | Budget key this shape consumes. |
| `zone_set` | Must name a `zone_sets` entry. |
| `metal` | Marks baremetal (slower boots; target of `*-metal` timeout rules). |
| `max_parallel` | Optional per-shape concurrency cap (0 = unlimited). |

## suites

```yaml
suites:
  imageboot: {vms: 4, timeout: 45m, est: 4m}
```

`timeout` must be set (> 0). `vms` is the per-job VM count;
`networks`/`subnets` optionally raise the job's network cost above 1.

## matrix

```yaml
matrix:
  - name: kernel-x86-vm          # config name; citrun -configs matches this
    shapes: [n1-standard-4, ...] # every shape must exist
    images: [rocky-linux-9, ...] # globs allowed; expanded against image_prefix
    suites: [imageboot]          # every suite must exist
    each_zone: false             # true: run the row once per zone in the
                                 # shape's zone_set (shape must have zones)
```

## rules

Exactly one action per rule; rules are validated (empty lists are errors).

| Action | Fields | Effect |
|---|---|---|
| `drop` | `shapes`, `suites`, `images` (globs) | Removes matching matrix cells. |
| `keep_only` | `shapes`, `suites` | Keeps only matching cells. |
| `suite_only_on` | `suites`, `shapes` | Restricts named suites to named shapes. |
| `zone_set` | `shapes`, `set` | Replaces the shapes' zone_set. |
| `timeout` | `shapes` (globs), `value` (duration) | Overrides suite timeout for matching shapes. |

## quarantine

Skips (or annotates) known-bad combinations:

```yaml
quarantine:
  - {image: rocky-linux-8*, shape: c3-standard-192-metal, reason: "boot hang"}
```

At least one of `image`/`shape`/`suite` must be non-empty.

## tiers

Named subsets pinned to explicit zones (e.g. fast smoke tiers):

```yaml
tiers:
  tier1:
    - {shape: c3-standard-4, zones: [us-central1-c]}
```

Every entry needs a known `shape` and non-empty `zones`; zones' regions must
have budgets; a shape may appear once per tier.

## shapevalidation

```yaml
shapevalidation:
  images: [...]
  arm_images: [...]
  families:
    - {name: c3, shape: c3-standard-4, cpus: 4, family: C3_CPUS,
       pinned_zone: us-central1-c}   # or zone_set: kernel-default
```

Each family needs exactly one of `pinned_zone` or `zone_set`, both of which
must have budget coverage.

## retries

Per-job retry caps and backoff. Caps count scheduler-classified failures;
when a cap is reached the job is marked `failed_infra` and not retried
(explicit `citrun resume` can retry it anyway). Omitted fields fall back to
the built-in defaults — most configs should not set this block at all.

```yaml
retries:
  stockout: 4        # completed zone sweeps before give-up
  quota: 4           # quota-failure attempts
  timeout: 2         # job-timeout attempts
  infra: 3           # Google API errors (412 races, 5xx)
  error: 2           # unclassified errors
  failure: 0         # retries after a real test failure
  cooldown_base: 120s
```

Defaults: `stockout` 4, `quota` 4, `timeout` 2, `infra` 3, `error` 2,
`failure` 0, `cooldown_base` 120s. Validation: every cap ≥ 1, `cooldown_base` > 0.

Semantics worth knowing:

- **Stockout counts sweeps, not attempts.** After a stockout the job retries
  in a different zone (measured: same-zone retry ~1% pass, different zone
  82%); a *sweep* completes when every eligible zone (budget-feasible, not
  yet blacklisted this round) has stocked out. Only completed sweeps
  increment the counter — a job with 10 zones can try ~40 zones over 4 sweeps
  before failing.
- **Backoff is exponential in completed sweeps**: `cooldown_base * 2^(n-1)`
  before sweep n+1 (2m, 4m, 8m at the default base). The same base drives the
  quota cooldown, which additionally doubles per consecutive hit per region
  and is clamped at 64m.
- **`failure` counts retries, not attempts** (unlike the other caps): a job
  that fails is requeued up to `failure` times, so `failure: 1` means two
  total attempts. Failed retries re-admit immediately (no cooldown, no zone
  avoidance — a flake is not a placement problem). When the cap is reached
  the job ends as `failed_real`, not `failed_infra`. `failure: 0` (the
  default) preserves the classic behavior of never retrying a real failure.
- **Counters reset when the config changes** mid-run (config refresh discards
  stockout sweeps/cooldowns) and on explicit resume of an exhausted job.
