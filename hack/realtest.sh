#!/usr/bin/env bash
# Real-kernel checks with memhog. Run inside Linux as root, with memhog and
# damon-probe on PATH:  bash hack/realtest.sh way1|way2
set -euo pipefail

TOTAL=${TOTAL:-1024}
HOT=${HOT:-200}
SAMPLES=${SAMPLES:-20}

case "${1:-}" in
way1)
	memhog -total "$TOTAL" -hot "$HOT" -addr '' >/tmp/memhog.log 2>&1 &
	pid=$!
	trap 'kill $pid 2>/dev/null' EXIT
	sleep 4
	cat /tmp/memhog.log
	grep VmRSS "/proc/$pid/status"
	damon-probe -pid "$pid" -count "$SAMPLES" -interval 1s
	;;
way2)
	root=/sys/fs/cgroup
	grep -qw memory "$root/cgroup.subtree_control" || echo +memory >"$root/cgroup.subtree_control"
	pids=()
	cgs=()
	for spec in "a:$HOT" "b:$((HOT / 2))"; do
		name=sader-test-${spec%%:*}
		hot=${spec##*:}
		mkdir -p "$root/$name"
		bash -c "echo \$\$ > $root/$name/cgroup.procs; exec memhog -total $TOTAL -hot $hot -addr ''" >"/tmp/$name.log" 2>&1 &
		pids+=($!)
		cgs+=("/$name")
	done
	trap 'kill ${pids[*]} 2>/dev/null; sleep 1; for c in ${cgs[*]}; do rmdir $root$c 2>/dev/null; done' EXIT
	sleep 6
	for c in "${cgs[@]}"; do
		echo "$c memory.current=$(( $(cat "$root$c/memory.current") >> 20 ))MiB"
	done
	damon-probe -cgroups "$(IFS=,; echo "${cgs[*]}")" -count "$SAMPLES" -interval 2s
	;;
*)
	sed -n '2,4p' "$0"
	exit 2
	;;
esac
