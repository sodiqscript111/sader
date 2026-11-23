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
gvisor)
	root=/sys/fs/cgroup
	cg=sader-gvisor
	grep -qw memory "$root/cgroup.subtree_control" || echo +memory >"$root/cgroup.subtree_control"
	mkdir -p "$root/$cg"
	bash -c "echo \$\$ > $root/$cg/cgroup.procs; exec runsc --network=none --platform=${PLATFORM:-systrap} do /usr/local/bin/memhog -total $TOTAL -hot $HOT -addr ''" >/tmp/gvisor-memhog.log 2>&1 &
	launcher=$!
	trap 'kill $launcher 2>/dev/null; sleep 2; pkill -f "memhog -total" 2>/dev/null; sleep 1; rmdir $root/$cg 2>/dev/null' EXIT
	sleep "${WARMUP:-15}"
	echo "== memhog log"; cat /tmp/gvisor-memhog.log
	sandbox=$(pgrep -f 'runsc-sandbox' | head -1)
	scg=$(cut -d: -f3 "/proc/$sandbox/cgroup")
	echo "== sandbox pid $sandbox is in cgroup $scg, memory.current=$(( $(cat "$root$scg/memory.current") >> 20 ))MiB"
	echo "== processes in $scg (pid rss_mib name)"
	best=0
	bestrss=0
	for p in $(cat "$root$scg/cgroup.procs"); do
		rss=$(( $(awk '/VmRSS/{print $2}' /proc/$p/status 2>/dev/null || echo 0) / 1024 ))
		cmd=$(tr '\0' ' ' </proc/$p/cmdline 2>/dev/null | cut -c1-60)
		printf "%s %s %s\n" "$p" "$rss" "${cmd:-(helper, no name)}"
		if [ -z "$cmd" ] && [ "$rss" -gt "$bestrss" ]; then best=$p; bestrss=$rss; fi
	done
	echo "== Way 2 on $scg"
	damon-probe -cgroups "$scg" -count "$SAMPLES" -interval 2s
	if [ "$best" != 0 ]; then
		echo "== Way 1 on biggest helper, pid $best"
		damon-probe -pid "$best" -count "$SAMPLES" -interval 1s
	fi
	;;
*)
	sed -n '2,4p' "$0"
	exit 2
	;;
esac
