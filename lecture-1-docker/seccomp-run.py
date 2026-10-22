#!/usr/bin/env python3
"""seccomp-run.py — применить seccomp-профиль и exec'нуть команду.

Использование: seccomp-run.py <profile.json> <команда> [аргументы...]

Профиль в формате Docker (--security-opt seccomp=...): поддерживаются
defaultAction и syscalls[].names/.action, действия SCMP_ACT_ALLOW,
SCMP_ACT_ERRNO (EPERM), SCMP_ACT_KILL. Фильтр наследуется через exec
и fork, поэтому действует на команду и всех её потомков.
Нужен пакет python3-seccomp (apt-get install -y python3-seccomp).
"""
import errno
import json
import os
import sys

import seccomp

ACTIONS = {
    "SCMP_ACT_ALLOW": seccomp.ALLOW,
    "SCMP_ACT_ERRNO": seccomp.ERRNO(errno.EPERM),
    "SCMP_ACT_KILL": seccomp.KILL,
}

if len(sys.argv) < 3:
    sys.exit(__doc__)

with open(sys.argv[1]) as f:
    profile = json.load(f)

flt = seccomp.SyscallFilter(defaction=ACTIONS[profile["defaultAction"]])
for rule in profile.get("syscalls", []):
    for name in rule["names"]:
        try:
            flt.add_rule(ACTIONS[rule["action"]], name)
        except RuntimeError:
            print(f"seccomp-run: unknown syscall {name!r}, skipped", file=sys.stderr)

flt.load()
os.execvp(sys.argv[2], sys.argv[2:])
