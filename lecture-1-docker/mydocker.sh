#!/usr/bin/env bash
set -euo pipefail

CG=/sys/fs/cgroup/mydocker
APP=/home/student/api
PROFILE=/lab/lecture-1-docker/seccomp.json
RUNNER=/lab/lecture-1-docker/seccomp-run.py
USER_NAME=student

export PROFILE;
export RUNNER;
export APP;

mkdir -p "$CG"
echo 200M > "$CG/memory.max"
echo 100M > "$CG/memory.swap.max"
echo "max 100000" > "$CG/cpu.max"
echo 20 > "$CG/pids.max"
echo $$ > "$CG/cgroup.procs"

setpriv --reuid=$USER_NAME --regid=$USER_NAME --init-groups \
 unshare --pid --fork --user --map-root-user --net --uts --mount --ipc --mount-proc \
 sh -c '
 ip link set lo up
 hostname mydocker
 exec setpriv --bounding-set=-all --inh-caps=-all --no-new-privs python3 "$RUNNER" "$PROFILE" "$APP"
 '