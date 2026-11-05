#!/usr/bin/env python3
"""Check that each Ansible task gives a Fabric script the NAMESPACE it needs.

channel-utils.sh exits with "NAMESPACE: unbound variable" (nounset) when
NAMESPACE is not set. Most scripts set NAMESPACE themselves before they
source it. A script that does not (upgrade-pauser.sh) needs the task that
runs it to pass NAMESPACE in its environment. The orderer and peer pauser
tasks did not, so every is_fabric_upgrade run failed after the first node
(found on platform-test, step 1 of the Fabric 3 upgrade).

The check: for each script in k8s_fabric_scripts that sources
channel-utils.sh before it assigns NAMESPACE, each task under
ansible-roles/*/tasks that runs the script must name NAMESPACE in its
environment or vars.

Usage: scripts/check-script-env.py   (exit 1 on a finding)
"""
import pathlib
import re
import sys

import yaml

ROOT = pathlib.Path(__file__).resolve().parent.parent
SCRIPTS = ROOT / "ansible-roles/k8s_fabric_scripts/files/scripts"
SOURCE = re.compile(r'^\s*(source|\.)\s+.*channel-utils\.sh')
ASSIGN = re.compile(r'^\s*(export\s+)?NAMESPACE=')


class Loader(yaml.SafeLoader):
    """SafeLoader that reads Ansible tags (!unsafe, !vault) as plain values."""


Loader.add_multi_constructor("!", lambda loader, suffix, node: loader.construct_scalar(node)
                             if isinstance(node, yaml.ScalarNode) else None)


def needs_namespace(path):
    """True when the script sources channel-utils.sh before it sets NAMESPACE."""
    for line in path.read_text().splitlines():
        if ASSIGN.match(line):
            return False
        if SOURCE.match(line):
            return True
    return False


def tasks(node):
    """Yield every task dict, also inside block/rescue/always."""
    if isinstance(node, list):
        for item in node:
            yield from tasks(item)
    elif isinstance(node, dict):
        yield node
        for key in ("block", "rescue", "always"):
            if key in node:
                yield from tasks(node[key])


def main():
    needy = sorted(p.name for p in SCRIPTS.glob("*.sh") if needs_namespace(p))
    findings = []
    for f in sorted(ROOT.glob("ansible-roles/*/tasks/*.y*ml")):
        for task in tasks(yaml.load(f.read_text(), Loader=Loader) or []):
            text = yaml.safe_dump({k: v for k, v in task.items()
                                   if k in ("command", "shell", "script", "ansible.builtin.command",
                                            "ansible.builtin.shell")})
            for name in needy:
                if "/" + name not in text:
                    continue
                env = yaml.safe_dump({k: task.get(k) for k in ("environment", "vars")})
                if not re.search(r'\bNAMESPACE\b', env):
                    findings.append(f"{f.relative_to(ROOT)}: task {task.get('name', '?')!r} "
                                    f"runs {name} without NAMESPACE")
    for line in findings:
        print(line)
    print(f"{len(needy)} script(s) need NAMESPACE from the task: {', '.join(needy) or 'none'}")
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
