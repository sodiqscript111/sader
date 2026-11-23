#!/usr/bin/env bash
# Builds the WSL2 kernel you are already running, with DAMON switched on.
# Run inside WSL as root:  bash hack/wsl-damon-kernel.sh <step>
#   deps    install build tools
#   prepare fetch the matching Microsoft source and enable DAMON
#   build   compile the kernel and its modules, pack modules into modules.vhdx
#   install copy bzImage-$NAME and modules-$NAME.vhdx to C:/wsl-kernel/
#
# By default it builds the version you run now. For a newer one (Way 2 needs
# 6.14+), pick a tag from github.com/microsoft/WSL2-Linux-Kernel:
#   TAG=linux-msft-wsl-6.18.58.1 SRC=/usr/src/wsl-damon-6.18 NAME=damon-6.18 \
#     bash hack/wsl-damon-kernel.sh prepare   (then build, install)
#
# Then point %UserProfile%/.wslconfig at them and run `wsl --shutdown`:
#   [wsl2]
#   kernel=C:\\wsl-kernel\\bzImage-<NAME>
#   kernelModules=C:\\wsl-kernel\\modules-<NAME>.vhdx
# Undo: delete those two lines and run `wsl --shutdown` again.
set -euo pipefail

VER=$(uname -r | sed 's/-microsoft-standard-WSL2.*//')
TAG=${TAG:-linux-msft-wsl-$VER}
SRC=${SRC:-/usr/src/wsl-damon}
CFG=Microsoft/config-wsl
OUT=${OUT:-/mnt/c/wsl-kernel}
NAME=${NAME:-damon}

case "${1:-}" in
deps)
	export DEBIAN_FRONTEND=noninteractive
	dpkg --configure -a
	apt-get update -qq
	apt-get install -y -qq build-essential flex bison dwarves libssl-dev libelf-dev cpio bc python3 git qemu-utils kmod rsync
	for c in gcc make flex bison pahole bc git qemu-img; do
		command -v "$c" >/dev/null || { echo "missing $c" >&2; exit 1; }
	done
	echo "deps ok"
	;;
prepare)
	if [ ! -d "$SRC/.git" ]; then
		git clone --depth 1 --branch "$TAG" https://github.com/microsoft/WSL2-Linux-Kernel.git "$SRC"
	fi
	cd "$SRC"
	# Keep "microsoft" in the release name: tools detect WSL by it.
	./scripts/config --file "$CFG" \
		-e DAMON -e DAMON_VADDR -e DAMON_PADDR -e DAMON_SYSFS \
		--set-str LOCALVERSION "-microsoft-standard-WSL2-damon"
	# Drivers for physical hardware WSL never sees; skipping them shortens
	# the build a lot. GPU access in WSL goes through DXGKRNL, which stays.
	./scripts/config --file "$CFG" \
		-d DRM_AMDGPU -d DRM_RADEON -d DRM_NOUVEAU -d DRM_I915 \
		-d INFINIBAND -d WLAN -d SND -d MEDIA_SUPPORT -d BT -d CAN
	make KCONFIG_CONFIG="$CFG" olddefconfig >/dev/null
	echo "--- DAMON options now:"
	grep -E '^(# )?CONFIG_DAMON' "$CFG"
	echo "--- loadable modules in config: $(grep -c '=m$' "$CFG")"
	;;
build)
	cd "$SRC"
	# Docker needs bridge and iptables, which this config builds as modules,
	# so build and pack the modules too (same steps as Microsoft's README).
	time make KCONFIG_CONFIG="$CFG" -j"$(nproc)"
	"$0" package
	;;
package)
	cd "$SRC"
	rel=$(make -s KCONFIG_CONFIG="$CFG" kernelrelease)
	rm -rf modules headers perf modules.vhdx
	make KCONFIG_CONFIG="$CFG" INSTALL_MOD_PATH="$PWD/modules" modules_install >/dev/null
	if [ -x Microsoft/scripts/gen_artifacts_vhdx.sh ]; then
		# 6.18+: one VHDX holding modules, UAPI headers and perf. perf is
		# optional tooling and slow to build, so ship an empty folder.
		make KCONFIG_CONFIG="$CFG" headers_install INSTALL_HDR_PATH="$PWD/headers" >/dev/null
		mkdir -p perf
		./Microsoft/scripts/gen_artifacts_vhdx.sh "$PWD/modules" "$PWD/headers" "$PWD/perf" "$rel" modules.vhdx
	else
		./Microsoft/scripts/gen_modules_vhdx.sh "$PWD/modules" "$rel" modules.vhdx
	fi
	echo "kernel release: $rel"
	ls -lh arch/x86/boot/bzImage modules.vhdx
	;;
install)
	mkdir -p "$OUT"
	cp "$SRC/arch/x86/boot/bzImage" "$OUT/bzImage-$NAME"
	cp "$SRC/modules.vhdx" "$OUT/modules-$NAME.vhdx"
	ls -lh "$OUT"
	;;
*)
	sed -n '2,18p' "$0"
	exit 2
	;;
esac
