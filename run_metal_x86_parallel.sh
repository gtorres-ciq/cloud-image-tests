#!/bin/bash
# Run every x86 image on the c3-standard-192-metal bare-metal shape with a
# CPU-BUDGET scheduler that queues jobs and admits them only when there is
# enough C3 quota, issuing `docker run` directly (not via run_tests.sh).
#
# Why a CPU budget (not a slot count): each test SUITE creates multiple metal
# VMs in one create-vms step, all consuming the per-region C3_CPUS quota at once.
# Audited VM counts per suite for a Rocky x86_64 image (each VM = 192 C3 CPUs)
# are in TEST_VMS. The scheduler tracks per-region in-flight CPUs and launches a
# suite only when its cost (VMs*192) fits the region's remaining budget; jobs
# that don't fit wait in the queue.
#
# Stockout-aware re-queue: the binding constraint for bare metal is usually
# physical capacity (ZONE_RESOURCE_POOL_EXHAUSTED), not quota. A job that fails
# with a stockout is RE-QUEUED with an escalating backoff (up to --max-retries
# times) so it waits for capacity instead of being marked failed; after the
# retry cap it is left failed for the next resume run. Real test failures (not
# stockouts) are NOT re-queued. When a region stocks out it is also put in a
# short cooldown (--region-cooldown) during which no new jobs are admitted
# there, so work steers to regions that still have capacity instead of piling
# onto a fast-failing one (a stockout fails in seconds and would otherwise look
# "most free" and attract more jobs).
#
# Resume-skip: a prior <image>_<shape>_<test>.xml is skipped only if it is a
# clean pass (junit with no <failure AND no <error). Run from the directory
# holding prior results (e.g. tmp-x86).
#
# Requires bash >= 5.1 (uses `wait -n -p`, $EPOCHSECONDS); Rocky/RHEL 9 = 5.1.8.
#
# Usage:
#   ./run_metal_x86_parallel.sh --dry-run
#   ./run_metal_x86_parallel.sh
#   ./run_metal_x86_parallel.sh --parallel 8 --max-retries 6 --region-cooldown 180
#
# Options:
#   --dry-run            Print plan, cost table, and docker commands; exit.
#   --max-retries N      Max STOCKOUT re-queues per job before giving up (default 3).
#   --backoff S          Per-job base backoff; the Nth retry waits S*N (default 120).
#   --region-cooldown S  After a region stocks out, don't admit there for S
#                        seconds (default 120). Steers jobs to regions with capacity.
#   --headroom N         C3 CPUs to reserve per region (budget = quota - N; default 0).
#   --parallel N         Hard cap on concurrent docker procs (default floor(budget/192)).
#                        Lower this to reduce self-contention for scarce metal hosts.
#   --project P          GCP project (default: ciq-test-servers).

set -uo pipefail

SHAPE="c3-standard-192-metal"
PROJECT="ciq-test-servers"
CPUS_PER_METAL=192
HEADROOM_CPUS=0
MAX_PARALLEL=""          # empty => auto (floor(total_budget / 192))
MAX_RETRIES=3            # stockout re-queues per job before giving up
BACKOFF_SECONDS=120      # per-job base backoff; retry N waits BACKOFF_SECONDS*N
REGION_COOLDOWN_SECONDS=120   # after a region stockout, skip that region this long
DRY_RUN=false

# Usable zones. europe-west1 and us-central1 both have 1920 C3_CPUS quota.
# europe-west1-c is left out (it stocked out ~3x more than -b); us-central1-a is
# the us-central1 zone with confirmed metal availability. To enable another
# region after a quota increase: raise its REGION_QUOTA and add its zone(s) here.
ZONES=(
    europe-west1-b
    us-central1-a
    us-east4-a
    us-east4-c
    us-west1-a
    us-west1-b
)

# Zone launch bias (only matters when a region has >1 zone in ZONES). Higher
# weight => more launches there. Kept for when europe-west1-c is re-added.
declare -A ZONE_WEIGHT=(
    [europe-west1-b]=2
    [europe-west1-c]=1
)

# C3-CPUS-per-project-region quota. Per-region admission budget = quota - headroom.
declare -A REGION_QUOTA=(
    [europe-west1]=1920
    [us-central1]=1920
    [us-east4]=1920
    [us-west1]=1920
)

# All x86 images (matches run_tests.sh X86_IMAGES). Delete lines to skip images.
IMAGES=(
    rocky-linux-8
    rocky-linux-9
    rocky-linux-10
    rocky-linux-8-optimized-gcp
    rocky-linux-9-optimized-gcp
    rocky-linux-10-optimized-gcp
    rocky-linux-8-optimized-gcp-nvidia-580
    rocky-linux-9-optimized-gcp-nvidia-580
    rocky-linux-10-optimized-gcp-nvidia-580
    rocky-linux-8-optimized-gcp-nvidia-latest
    rocky-linux-9-optimized-gcp-nvidia-latest
    rocky-linux-10-optimized-gcp-nvidia-latest
)

# Tests applicable to c3-standard-192-metal. Excluded (per run_tests.sh): cvm,
# livemigrate, suspendresume, imageboot, network, disk, lssd, vmspec.
TESTS=(
    guestagent
    hostnamevalidation
    hotattach
    licensevalidation
    loadbalancer
    metadata
    packagevalidation
    security
    ssh
)

# Metal VMs per suite for a Rocky x86_64 image (audited 2026-06-14; every VM =
# 192 C3 CPUs). Admission cost = TEST_VMS * 192. metadata=7 assumes NO
# --custom_startup_script (that flag collapses metadata to 1 VM).
declare -A TEST_VMS=(
    [guestagent]=3
    [hostnamevalidation]=2
    [hotattach]=1
    [licensevalidation]=1
    [loadbalancer]=6
    [metadata]=7
    [packagevalidation]=1
    [security]=1
    [ssh]=4
)

usage() { awk 'NR==1{next} /^#/{sub(/^# ?/,""); print; next} {exit}' "$0"; }

while [[ $# -gt 0 ]]; do
    case $1 in
        --dry-run) DRY_RUN=true; shift ;;
        --max-retries)
            if [[ -z "${2:-}" || ! "${2}" =~ ^[0-9]+$ ]]; then echo "Error: --max-retries needs a non-negative integer." >&2; exit 1; fi
            MAX_RETRIES="$2"; shift 2 ;;
        --backoff)
            if [[ -z "${2:-}" || ! "${2}" =~ ^[0-9]+$ ]]; then echo "Error: --backoff needs a non-negative integer." >&2; exit 1; fi
            BACKOFF_SECONDS="$2"; shift 2 ;;
        --region-cooldown)
            if [[ -z "${2:-}" || ! "${2}" =~ ^[0-9]+$ ]]; then echo "Error: --region-cooldown needs a non-negative integer." >&2; exit 1; fi
            REGION_COOLDOWN_SECONDS="$2"; shift 2 ;;
        --headroom)
            if [[ -z "${2:-}" || ! "${2}" =~ ^[0-9]+$ ]]; then echo "Error: --headroom needs a non-negative integer." >&2; exit 1; fi
            HEADROOM_CPUS="$2"; shift 2 ;;
        --parallel)
            if [[ -z "${2:-}" || ! "${2}" =~ ^[1-9][0-9]*$ ]]; then echo "Error: --parallel needs a positive integer." >&2; exit 1; fi
            MAX_PARALLEL="$2"; shift 2 ;;
        --project)
            if [[ -z "${2:-}" ]]; then echo "Error: --project requires a value." >&2; exit 1; fi
            PROJECT="$2"; shift 2 ;;
        --help|-h) usage; exit 0 ;;
        *) echo "Unknown argument: $1 (try --help)" >&2; exit 1 ;;
    esac
done

# --- Per-region budgets and a weighted zone "bag" (prefer-b) -----------------
declare -A REGION_ZONES REGION_BUDGET REGION_INFLIGHT_CPUS REGION_RR REGION_ZONE_BAG REGION_NOT_BEFORE
for zone in "${ZONES[@]}"; do
    region="${zone%-*}"
    REGION_ZONES[$region]="${REGION_ZONES[$region]:-}$zone "
done

total_budget=0; max_budget=0
for region in "${!REGION_ZONES[@]}"; do
    quota="${REGION_QUOTA[$region]:-0}"
    budget=$(( quota - HEADROOM_CPUS ))
    REGION_INFLIGHT_CPUS[$region]=0
    REGION_RR[$region]=0
    REGION_NOT_BEFORE[$region]=0
    if (( budget < CPUS_PER_METAL )); then
        echo "Note: skipping ${region} (budget ${budget} < ${CPUS_PER_METAL} for one VM)." >&2
        continue
    fi
    REGION_BUDGET[$region]=$budget
    total_budget=$(( total_budget + budget ))
    (( budget > max_budget )) && max_budget=$budget
    # Build the weighted zone bag (each zone repeated by its weight).
    bag=""
    read -ra _zs <<< "${REGION_ZONES[$region]}"
    for z in "${_zs[@]}"; do
        w="${ZONE_WEIGHT[$z]:-1}"
        for (( k=0; k<w; k++ )); do bag+="$z "; done
    done
    REGION_ZONE_BAG[$region]="$bag"
done

if (( ${#REGION_BUDGET[@]} == 0 )); then
    echo "ERROR: no usable region (no region's budget fits a ${CPUS_PER_METAL}-CPU VM)." >&2
    exit 1
fi
if [[ -z "$MAX_PARALLEL" ]]; then MAX_PARALLEL=$(( total_budget / CPUS_PER_METAL )); fi

# --- Per-test cost; flag a headroom that hides the biggest suite -------------
declare -A TEST_COST
largest_cost=0
for testrun in "${TESTS[@]}"; do
    vms="${TEST_VMS[$testrun]:-}"
    if [[ -z "$vms" ]]; then echo "WARNING: no TEST_VMS entry for '${testrun}'; assuming 1 VM. Audit it." >&2; vms=1; fi
    cost=$(( vms * CPUS_PER_METAL )); TEST_COST[$testrun]=$cost
    (( cost > largest_cost )) && largest_cost=$cost
done
if (( largest_cost > max_budget )); then
    echo "WARNING: largest suite costs ${largest_cost} CPUs but max region budget is ${max_budget};" >&2
    (( HEADROOM_CPUS > 0 )) && echo "         --headroom ${HEADROOM_CPUS} is likely too high (those suites will be skipped)." >&2
fi

# A prior result is a skippable pass only if it is real junit (<testcase) with
# no <failure AND no <error.
result_is_pass() {
    local f="$1"
    [ -s "$f" ] || return 1
    grep -q "<testcase" "$f" || return 1
    grep -qE "<(failure|error)" "$f" && return 1
    return 0
}
# A finished job stocked out (transient capacity) iff its output names it.
is_stockout() { [ -s "$1" ] && grep -qi 'ZONE_RESOURCE_POOL_EXHAUSTED' "$1"; }

# --- Build the queue (resume-skip + unschedulable check) ---------------------
declare -a JOB_IMG JOB_TEST JOB_OUT JOB_COST
skipped=0; unschedulable=0; total_cpu_work=0
for image in "${IMAGES[@]}"; do
    for testrun in "${TESTS[@]}"; do
        out="${image}_${SHAPE}_${testrun}.xml"
        if result_is_pass "$out"; then skipped=$(( skipped + 1 )); continue; fi
        cost=${TEST_COST[$testrun]}
        if (( cost > max_budget )); then
            echo "SKIP (needs quota increase): ${testrun} on ${image} costs ${cost} > max budget ${max_budget}" >&2
            unschedulable=$(( unschedulable + 1 )); continue
        fi
        [ -s "$out" ] && echo "Re-queuing (prior run errored/failed or incomplete): ${testrun} on ${image}" >&2
        JOB_IMG+=("$image"); JOB_TEST+=("$testrun"); JOB_OUT+=("$out"); JOB_COST+=("$cost")
        total_cpu_work=$(( total_cpu_work + cost ))
    done
done
njobs=${#JOB_IMG[@]}

build_cmd() {
    local image="$1" testrun="$2" zone="$3"
    local full_image="projects/gce-ciq-images/global/images/family/$image"
    CMD=(docker run --rm
        -v "$PWD:/curpath:z"
        -v "$HOME/.config/gcloud/:/creds:z"
        -e GOOGLE_APPLICATION_CREDENTIALS=/creds/application_default_credentials.json
        cloud-image-tests
        --project "$PROJECT"
        --filter "^(${testrun})$"
        --zones "$zone"
        --images "$full_image"
        -x86_shape="$SHAPE"
        --parallel_count 1)
}

# --- Plan summary ------------------------------------------------------------
echo "=== CPU-budget plan (metal VM = ${CPUS_PER_METAL} CPUs; headroom ${HEADROOM_CPUS}; max-retries ${MAX_RETRIES}; backoff ${BACKOFF_SECONDS}s; region-cooldown ${REGION_COOLDOWN_SECONDS}s) ===" >&2
for region in $(printf '%s\n' "${!REGION_BUDGET[@]}" | sort); do
    printf '  %-16s budget=%-5d zone-bag=[%s]\n' "$region" "${REGION_BUDGET[$region]}" "${REGION_ZONE_BAG[$region]% }" >&2
done
echo "  per-suite cost:" >&2
for testrun in "${TESTS[@]}"; do
    printf '    %-20s %d VMs = %5d CPUs\n' "$testrun" "${TEST_VMS[$testrun]:-1}" "${TEST_COST[$testrun]}" >&2
done
echo "  queued=${njobs}  skipped(passed)=${skipped}  unschedulable=${unschedulable}  total_cpu_work=${total_cpu_work}  max_procs=${MAX_PARALLEL}" >&2
if (( njobs == 0 )); then echo "Nothing to run." >&2; exit 0; fi

# --- Dry run -----------------------------------------------------------------
if $DRY_RUN; then
    bag_all=()
    for region in $(printf '%s\n' "${!REGION_BUDGET[@]}" | sort); do
        read -ra zz <<< "${REGION_ZONE_BAG[$region]}"; bag_all+=("${zz[@]}")
    done
    echo "# (preview-zone is illustrative; at runtime the scheduler admits by CPU" >&2
    echo "#  budget, re-queues stockouts, and cools a region that just stocked out.)" >&2
    for (( j=0; j<njobs; j++ )); do
        zone="${bag_all[ j % ${#bag_all[@]} ]}"
        build_cmd "${JOB_IMG[$j]}" "${JOB_TEST[$j]}" "$zone"
        printf '# %-12s | %-18s | cost=%-5d | preview-zone=%s\n' "${JOB_IMG[$j]}" "${JOB_TEST[$j]}" "${JOB_COST[$j]}" "$zone"
        printf '%q ' "${CMD[@]}"; printf '| tee %q\n\n' "${JOB_OUT[$j]}"
    done
    echo "# dry-run: ${njobs} jobs queued; CPU-budget admission + stockout re-queue + region cooldown." >&2
    exit 0
fi

# --- Real run: preflight, then dispatch --------------------------------------
if (( BASH_VERSINFO[0] < 5 || (BASH_VERSINFO[0] == 5 && BASH_VERSINFO[1] < 1) )); then
    echo "ERROR: needs bash >= 5.1 for 'wait -n -p' / \$EPOCHSECONDS (you have ${BASH_VERSION})." >&2; exit 1
fi
if ! docker image inspect cloud-image-tests >/dev/null 2>&1; then
    echo "ERROR: docker image 'cloud-image-tests' not found locally." >&2
    echo "Build it first: docker build -t cloud-image-tests -f Dockerfile ." >&2; exit 1
fi

# run_one is ONE backgrounded subshell wrapping docker|tee, so $! is its pid and
# `wait -n -p` returns exactly that pid (validated).
run_one() {
    local image="$1" testrun="$2" zone="$3" out="$4"
    build_cmd "$image" "$testrun" "$zone"
    "${CMD[@]}" | tee "$out"
}

declare -A PID_REGION PID_COST PID_JOB
declare -a JOB_SCHEDULED JOB_RETRIES JOB_NOT_BEFORE
for (( i=0; i<njobs; i++ )); do JOB_SCHEDULED[i]=0; JOB_RETRIES[i]=0; JOB_NOT_BEFORE[i]=0; done
remaining=$njobs; total_inflight=0; donepid=""; total_retries=0; gaveup=0

while (( remaining > 0 )) || (( total_inflight > 0 )); do
    now=$EPOCHSECONDS
    placed_any=0
    for (( idx=0; idx<njobs; idx++ )); do
        (( JOB_SCHEDULED[idx] == 1 )) && continue
        (( JOB_NOT_BEFORE[idx] > now )) && continue          # job still backing off
        (( total_inflight < MAX_PARALLEL )) || break
        cost=${JOB_COST[idx]}
        # Most-free region that fits AND is not in a stockout cooldown.
        placed=""; best_free=-1
        for region in "${!REGION_BUDGET[@]}"; do
            (( ${REGION_NOT_BEFORE[$region]:-0} > now )) && continue   # region cooling down
            free=$(( REGION_BUDGET[$region] - REGION_INFLIGHT_CPUS[$region] ))
            if (( cost <= free && free > best_free )); then best_free=$free; placed="$region"; fi
        done
        [[ -n "$placed" ]] || continue                       # no eligible region right now
        read -ra zbag <<< "${REGION_ZONE_BAG[$placed]}"
        zi=$(( REGION_RR[$placed] % ${#zbag[@]} )); zone="${zbag[$zi]}"
        REGION_RR[$placed]=$(( REGION_RR[$placed] + 1 ))
        run_one "${JOB_IMG[$idx]}" "${JOB_TEST[$idx]}" "$zone" "${JOB_OUT[$idx]}" &
        pid=$!
        PID_REGION[$pid]="$placed"; PID_COST[$pid]=$cost; PID_JOB[$pid]=$idx
        REGION_INFLIGHT_CPUS[$placed]=$(( REGION_INFLIGHT_CPUS[$placed] + cost ))
        total_inflight=$(( total_inflight + 1 ))
        JOB_SCHEDULED[idx]=1; remaining=$(( remaining - 1 )); placed_any=1
        echo "Launch [${placed} ${REGION_INFLIGHT_CPUS[$placed]}/${REGION_BUDGET[$placed]} CPUs, ${total_inflight} running]: ${JOB_TEST[$idx]} on ${JOB_IMG[$idx]} in ${zone} (attempt $(( JOB_RETRIES[idx] + 1 )))" >&2
    done

    if (( total_inflight > 0 )); then
        wait -n -p donepid
        total_inflight=$(( total_inflight - 1 ))
        r="${PID_REGION[$donepid]:-}"; idx="${PID_JOB[$donepid]:-}"; c="${PID_COST[$donepid]:-0}"
        if [[ -z "$r" ]]; then
            echo "BUG: reaped untracked pid '${donepid}'" >&2
        else
            REGION_INFLIGHT_CPUS[$r]=$(( REGION_INFLIGHT_CPUS[$r] - c ))
            unset 'PID_REGION[$donepid]' 'PID_COST[$donepid]' 'PID_JOB[$donepid]'
            if [[ -n "$idx" ]] && is_stockout "${JOB_OUT[$idx]}"; then
                REGION_NOT_BEFORE[$r]=$(( EPOCHSECONDS + REGION_COOLDOWN_SECONDS ))   # cool the region
                if (( JOB_RETRIES[idx] < MAX_RETRIES )); then
                    JOB_RETRIES[idx]=$(( JOB_RETRIES[idx] + 1 )); total_retries=$(( total_retries + 1 ))
                    JOB_SCHEDULED[idx]=0; remaining=$(( remaining + 1 ))
                    JOB_NOT_BEFORE[idx]=$(( EPOCHSECONDS + BACKOFF_SECONDS * JOB_RETRIES[idx] ))
                    echo "Stockout in ${r} (cooling ${REGION_COOLDOWN_SECONDS}s): re-queue ${JOB_TEST[idx]} on ${JOB_IMG[idx]} (retry ${JOB_RETRIES[idx]}/${MAX_RETRIES}, wait $(( BACKOFF_SECONDS * JOB_RETRIES[idx] ))s)" >&2
                else
                    gaveup=$(( gaveup + 1 ))
                    echo "Stockout in ${r} (cooling ${REGION_COOLDOWN_SECONDS}s): GAVE UP on ${JOB_TEST[idx]} on ${JOB_IMG[idx]} after ${MAX_RETRIES} retries (left failed for next resume run)" >&2
                fi
            fi
        fi
    elif (( remaining > 0 )); then
        # Nothing running and nothing admitted -> all remaining jobs are waiting
        # on a job backoff and/or every fitting region is cooling down. Sleep
        # until the earliest of those expires, then re-attempt.
        now=$EPOCHSECONDS
        earliest=0
        for (( widx=0; widx<njobs; widx++ )); do
            (( JOB_SCHEDULED[widx] == 1 )) && continue
            nb=${JOB_NOT_BEFORE[widx]}
            if (( nb > now )); then (( earliest == 0 || nb < earliest )) && earliest=$nb; fi
        done
        for region in "${!REGION_BUDGET[@]}"; do
            rb=${REGION_NOT_BEFORE[$region]}
            if (( rb > now )); then (( earliest == 0 || rb < earliest )) && earliest=$rb; fi
        done
        if (( earliest > 0 )); then
            nap=$(( earliest - now ))
            echo "All ${remaining} queued job(s) waiting (job backoff or region cooldown); sleeping ${nap}s for capacity..." >&2
            sleep "$nap"
        else
            echo "ERROR: ${remaining} job(s) unplaceable and nothing is waiting; aborting to avoid an infinite loop." >&2
            break
        fi
    fi
done
wait
echo "Done. dispatched ${njobs} job(s); stockout re-queues=${total_retries}; gave-up(after ${MAX_RETRIES} retries)=${gaveup}; skipped(passed)=${skipped}; skipped(quota)=${unschedulable}." >&2
