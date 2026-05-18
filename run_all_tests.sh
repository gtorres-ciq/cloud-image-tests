#!/bin/bash
# Run cloud-image-tests across x86, ARM, and ARM-metal configurations in parallel.
#
# Usage:
#   ./run_all_tests.sh [--configs x86,arm,arm-metal] [--dry-run] [--summary-only]

set -euo pipefail

START_SECONDS=$SECONDS
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_DIR="$SCRIPT_DIR/tmp-logs/$(date +%Y%m%d-%H%M%S)"
DRY_RUN=false
SUMMARY_ONLY=false

VALID_CONFIGS=(x86 arm arm-metal)
CONFIGS=("${VALID_CONFIGS[@]}")
EXTRA_ARGS=()

while [[ $# -gt 0 ]]; do
	case "$1" in
	--configs)
		IFS=',' read -ra CONFIGS <<<"$2"
		shift 2
		;;
	--dry-run)
		DRY_RUN=true
		shift
		;;
	--summary-only)
		SUMMARY_ONLY=true
		shift
		;;
	--help | -h)
		echo "Usage: $0 [--configs x86,arm,arm-metal] [--dry-run] [--summary-only] [run_tests.sh flags...]"
		echo ""
		echo "Options:"
		echo "  --configs <c1,c2,...>  Comma-separated configs to run (default: all)"
		echo "  --dry-run             Print what would run, then exit"
		echo "  --summary-only        Show results from existing test output, skip running tests"
		echo ""
		echo "Extra flags (forwarded to run_tests.sh):"
		echo "  --regions <z1,z2,...>  Override zones for all configs"
		echo "  --tests <t1,t2,...>    Override which tests to run"
		echo "  --images <i1,i2,...>   Override which images to test"
		echo "  --parallel <count>     Parallel test count"
		echo ""
		echo "Valid configs: ${VALID_CONFIGS[*]}"
		exit 0
		;;
	*)
		EXTRA_ARGS+=("$1")
		shift
		if [[ $# -gt 0 && ! "$1" == --* ]]; then
			EXTRA_ARGS+=("$1")
			shift
		fi
		continue
		;;
	esac
done

for cfg in "${CONFIGS[@]}"; do
	valid=false
	for v in "${VALID_CONFIGS[@]}"; do
		[[ "$cfg" == "$v" ]] && valid=true && break
	done
	if ! $valid; then
		echo "ERROR: Unknown config '$cfg'. Valid: ${VALID_CONFIGS[*]}" >&2
		exit 1
	fi
done

config_dir() {
	case "$1" in
	x86) echo "$SCRIPT_DIR/tmp-x86" ;;
	arm) echo "$SCRIPT_DIR/tmp-arm" ;;
	arm-metal) echo "$SCRIPT_DIR/tmp-arm-metal" ;;
	esac
}

config_args() {
	case "$1" in
	x86) ARGS=() ;;
	arm) ARGS=(--arm) ;;
	arm-metal) ARGS=(--arm --shapes c4a-highmem-96-metal -custom_startup_script=/curpath/startup.sh) ;;
	esac
}

write_startup_script() {
	cat >"$1" <<'STARTUP_EOF'
#!/bin/bash
systemctl stop chronyd
count=0
while [ 1 -eq 1 ]; do
    chronyd -q
    if [ "$?" -eq 0 ]; then
        echo "Time synchronized successfully."
        break
    fi
    count=$((count + 1))
    if [ $count -gt 10 ]; then
        echo "Failed to synchronize time after 10 attempts. Exiting."
        systemctl start chronyd
        exit 1
    fi
    sleep 5
done
systemctl start chronyd
STARTUP_EOF
	chmod +x "$1"
}

# ---------------------------------------------------------------------------
# Dry run
# ---------------------------------------------------------------------------
if $DRY_RUN; then
	echo "=== Dry run ==="
	for cfg in "${CONFIGS[@]}"; do
		dir="$(config_dir "$cfg")"
		config_args "$cfg"
		printf '  [%s]\n' "$cfg"
		printf '    dir:  %s\n' "$dir"
		if [[ "$cfg" == "arm-metal" ]]; then
			printf '    pre:  write startup.sh to %s/startup.sh\n' "$dir"
		fi
		printf '    cmd:  cd %s && %s/run_tests.sh %s %s\n\n' "$dir" "$SCRIPT_DIR" "${ARGS[*]}" "${EXTRA_ARGS[*]+"${EXTRA_ARGS[*]}"}"
	done
	exit 0
fi

declare -A EXIT_CODES

if ! $SUMMARY_ONLY; then

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------
for cfg in "${CONFIGS[@]}"; do
	mkdir -p "$(config_dir "$cfg")"
done
mkdir -p "$LOG_DIR"

for cfg in "${CONFIGS[@]}"; do
	if [[ "$cfg" == "arm-metal" ]]; then
		write_startup_script "$(config_dir "$cfg")/startup.sh"
		echo "Wrote startup.sh to $(config_dir "$cfg")/startup.sh"
	fi
done

# ---------------------------------------------------------------------------
# Launch pipelines
# ---------------------------------------------------------------------------
declare -A PIDS START_TIMES
echo "=== Launching ${#CONFIGS[@]} test configurations (logs=$LOG_DIR) ==="
for cfg in "${CONFIGS[@]}"; do
	dir="$(config_dir "$cfg")"
	config_args "$cfg"
	log="$LOG_DIR/${cfg}.log"

	(cd "$dir" && "$SCRIPT_DIR/run_tests.sh" "${ARGS[@]}" ${EXTRA_ARGS[@]+"${EXTRA_ARGS[@]}"}) >"$log" 2>&1 &
	PIDS[$cfg]=$!
	START_TIMES[$cfg]=$SECONDS
	echo "  + $cfg (PID ${PIDS[$cfg]}, log: $log)"
done

# ---------------------------------------------------------------------------
# Wait — poll PIDs and report as each config finishes.
# ---------------------------------------------------------------------------
echo ""
echo "=== Waiting for ${#CONFIGS[@]} configurations ==="
declare -A DURATIONS
remaining=("${CONFIGS[@]}")
while [[ ${#remaining[@]} -gt 0 ]]; do
	for i in "${!remaining[@]}"; do
		cfg="${remaining[$i]}"
		if ! kill -0 "${PIDS[$cfg]}" 2>/dev/null; then
			wait "${PIDS[$cfg]}" && EXIT_CODES[$cfg]=0 || EXIT_CODES[$cfg]=$?
			DURATIONS[$cfg]=$((SECONDS - START_TIMES[$cfg]))
			elapsed="${DURATIONS[$cfg]}"
			printf '  [%s] completed (exit %d, %dm %02ds)\n' \
				"$cfg" "${EXIT_CODES[$cfg]}" $((elapsed / 60)) $((elapsed % 60))
			unset 'remaining[$i]'
			if [[ ${#remaining[@]} -gt 0 ]]; then
				echo "  Still running: ${remaining[*]}"
			fi
		fi
	done
	if [[ ${#remaining[@]} -gt 0 ]]; then
		sleep 30
	fi
done

fi # end ! $SUMMARY_ONLY

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo ""
echo "=== Results ==="
printf '  %-15s  %-30s  %s\n' "CONFIG" "STATUS" "TESTS"
printf '  %-15s  %-30s  %s\n' "------" "------" "-----"

overall_fail=0
declare -A STATUSES
for cfg in "${CONFIGS[@]}"; do
	dir="$(config_dir "$cfg")"

	xml_total=0
	xml_pass=0
	xml_fail=0
	has_xml_failure=0
	while IFS= read -r -d '' xml; do
		((xml_total++)) || true
		if grep -q '<failure ' "$xml" 2>/dev/null; then
			has_xml_failure=1
			((xml_fail++)) || true
		else
			((xml_pass++)) || true
		fi
	done < <(find "$dir" -name '*.xml' -size +0 -print0 2>/dev/null)

	exit_code="${EXIT_CODES[$cfg]:-0}"
	if [[ "$has_xml_failure" -eq 1 ]]; then
		status="FAIL (test failures)"
		((overall_fail++)) || true
	elif [[ "$exit_code" -ne 0 ]]; then
		status="FAIL (exit $exit_code)"
		((overall_fail++)) || true
	elif [[ "$xml_total" -eq 0 ]]; then
		status="NO RESULTS"
		((overall_fail++)) || true
	else
		status="PASS"
	fi
	STATUSES[$cfg]="$status"

	printf '  %-15s  %-30s  %d passed, %d failed (%d total)\n' \
		"$cfg" "$status" "$xml_pass" "$xml_fail" "$xml_total"
done

# ---------------------------------------------------------------------------
# Failure details
# ---------------------------------------------------------------------------
infra_total=0
real_total=0

if [[ $overall_fail -gt 0 ]]; then
	echo ""
	echo "=== Failures ==="
	for cfg in "${CONFIGS[@]}"; do
		[[ "${STATUSES[$cfg]}" == "PASS" ]] && continue
		dir="$(config_dir "$cfg")"
		echo "  [$cfg]"
		while IFS= read -r -d '' xml; do
			if grep -q '<failure' "$xml" 2>/dev/null; then
				base=$(basename "$xml" .xml)
				suite="${base##*_}"
				rest="${base%_*}"
				shape="${rest##*_}"
				image="${rest%_*}"
				echo "    [$shape] $image / $suite:"
				awk_out=$(awk '
				/<testcase / {
					if (match($0, / name="[^"]*"/)) {
						name = substr($0, RSTART+7, RLENGTH-8)
					}
				}
				/<failure/ {
					code = ""; msg = ""; attr = ""; cdata = ""
					in_fail = 1
					if (match($0, /message="[^"]+"/)) {
						attr = substr($0, RSTART+9, RLENGTH-10)
					}
					if (match($0, /CDATA\[.+/)) {
						cdata = substr($0, RSTART+6)
						gsub(/\]\]>.*/, "", cdata)
					}
				}
				in_fail && /Code: / {
					code = $0
					sub(/.*Code: /, "", code)
					gsub(/[[:space:]]+$/, "", code)
					sub(/_WITH_DETAILS$/, "", code)
				}
				in_fail && /Message: / {
					msg = $0
					sub(/.*Message: /, "", msg)
					gsub(/\]\]>.*/, "", msg)
					gsub(/[[:space:]]+$/, "", msg)
					if (match(msg, /zones\/[^'\'' ]+/)) {
						msg = substr(msg, RSTART+6, RLENGTH-6)
					} else if (length(msg) > 80) {
						msg = substr(msg, 1, 80) "..."
					}
				}
				/<\/failure/ {
					in_fail = 0
					if (code != "") {
						reason = code " (" msg ")"
						is_infra = 1
					} else if (attr != "") {
						reason = attr
						is_infra = 0
					} else if (cdata != "") {
						gsub(/^[[:space:]]+/, "", cdata)
						if (length(cdata) > 80) cdata = substr(cdata, 1, 80) "..."
						reason = cdata
						is_infra = 0
					} else {
						reason = "Failed"
						is_infra = 0
					}
					if (!(reason in rcount)) {
						rfirst[reason] = name
						rinfra[reason] = is_infra
					}
					rcount[reason]++
				}
				END {
					infra = 0; real = 0
					for (r in rcount) {
						if (rinfra[r])
							infra += rcount[r]
						else
							real += rcount[r]
						if (rcount[r] > 1)
							printf "      %s (%d tests)\n", r, rcount[r]
						else
							printf "      %s: %s\n", rfirst[r], r
					}
					printf "###COUNTS %d %d\n", infra, real
				}
				' "$xml")
				while IFS= read -r line; do
					if [[ "$line" == "###COUNTS "* ]]; then
						read -r _ i r <<< "$line"
						((infra_total += i)) || true
						((real_total += r)) || true
					elif [[ -n "$line" ]]; then
						echo "$line"
					fi
				done <<< "$awk_out"
			fi
		done < <(find "$dir" -name '*.xml' -print0 2>/dev/null | sort -z)
	done

	echo ""
	echo "=== Failure Breakdown ==="
	printf '  Infra (VM did not launch): %d tests\n' "$infra_total"
	printf '  Actual test failures:      %d tests\n' "$real_total"
fi

echo ""
echo "Logs: $LOG_DIR"
elapsed=$((SECONDS - START_SECONDS))
printf 'Total time: %dm %02ds\n' $((elapsed / 60)) $((elapsed % 60))

[[ $overall_fail -eq 0 ]]
