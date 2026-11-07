#!/usr/bin/env python3
"""Check that the demo-only chmod in docker-compose.yaml stays demo-only.

    docker compose config --format json | scripts/check_demo_init.py <repo>

The one-shot service demo-init runs as root and makes directories writable
for everyone, so that the adapters (uid 10001) can write their state and
journals on any host. That is acceptable only for the demo directories. This
check fails when demo-init could reach anything else:

- every volume is a bind mount whose source lies below <repo>/.demo/,
- the command is a plain, non-recursive chmod of exactly its mount targets,
- the service has no network.

Stdlib only, Python 3.7+.
"""

import json
import os
import sys


def problems(config, repo):
    demo_root = os.path.realpath(os.path.join(repo, ".demo"))
    service = (config.get("services") or {}).get("demo-init")
    if service is None:
        return ["docker-compose.yaml has no demo-init service"]
    found = []
    targets = []
    for vol in service.get("volumes") or []:
        if vol.get("type") != "bind":
            found.append("volume %r is not a bind mount" % (vol,))
            continue
        source = os.path.realpath(vol.get("source", ""))
        if os.path.commonpath([source, demo_root]) != demo_root or source == demo_root:
            found.append("volume source %s is not below %s" % (source, demo_root))
        targets.append(vol.get("target", ""))
    if not targets:
        found.append("demo-init mounts nothing")
    command = service.get("command") or []
    if isinstance(command, str):
        command = command.split()
    entrypoint = service.get("entrypoint")
    if entrypoint:
        found.append("demo-init sets an entrypoint %r; the check expects the image's default" % (entrypoint,))
    if len(command) < 3 or command[0] != "chmod":
        found.append("command %r is not chmod <mode> <paths>" % (command,))
    else:
        mode, paths = command[1], command[2:]
        if mode.startswith("-") or any(p.startswith("-") for p in paths):
            found.append("chmod takes options in %r; only a plain, non-recursive chmod is allowed" % (command,))
        for path in paths:
            if path not in targets:
                found.append("chmod path %s is not one of the demo mounts %s" % (path, targets))
    if service.get("network_mode") != "none":
        found.append("demo-init has a network (network_mode %r)" % (service.get("network_mode"),))
    return found


def main(argv):
    if len(argv) != 2:
        print(__doc__.strip().splitlines()[2].strip(), file=sys.stderr)
        return 2
    config = json.load(sys.stdin)
    found = problems(config, argv[1])
    for p in found:
        print("demo-init: " + p, file=sys.stderr)
    if found:
        return 1
    print("demo-init: chmod limited to the demo directories")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
