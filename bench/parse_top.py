#!/usr/bin/env python3
"""Reduce raw `top -l N -stats pid,cpu,power` output to one CSV row per sample.

usage: parse_top.py <raw-top-file> <pid> [<pid> ...]
prints: sample,tool_cpu_pct,tool_power,sys_cpu_busy_pct,sys_mem_used_mb

The first sample is dropped: top has no interval to compute CPU over yet.
"""
import re
import sys


def to_mb(text):
    m = re.match(r"([\d.]+)([KMGT])", text)
    if not m:
        return 0.0
    scale = {"K": 1 / 1024, "M": 1, "G": 1024, "T": 1024 * 1024}[m.group(2)]
    return float(m.group(1)) * scale


def main():
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        return 1
    pids = set(sys.argv[2:])
    samples = []
    cur = None
    with open(sys.argv[1], errors="replace") as fh:
        for line in fh:
            if line.startswith("Processes:"):
                cur = {"cpu": 0.0, "power": 0.0, "busy": 0.0, "mem": 0.0}
                samples.append(cur)
                continue
            if cur is None:
                continue
            if line.startswith("CPU usage:"):
                m = re.search(r"([\d.]+)% idle", line)
                if m:
                    cur["busy"] = 100.0 - float(m.group(1))
                continue
            if line.startswith("PhysMem:"):
                m = re.match(r"PhysMem: (\S+) used", line)
                if m:
                    cur["mem"] = to_mb(m.group(1))
                continue
            parts = line.split()
            if len(parts) >= 3 and parts[0] in pids:
                try:
                    cur["cpu"] += float(parts[1])
                    cur["power"] += float(parts[2])
                except ValueError:
                    pass
    print("sample,tool_cpu_pct,tool_power,sys_cpu_busy_pct,sys_mem_used_mb")
    for i, s in enumerate(samples[1:], start=1):
        print(f"{i},{s['cpu']:.1f},{s['power']:.1f},{s['busy']:.1f},{s['mem']:.0f}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
