#!/usr/bin/env python3
"""Reduce `powermetrics -f plist` output to one CSV row per sample.

usage: parse_power.py <raw-plist-file> <pid> [<pid> ...]
prints: sample,combined_mw,cpu_mw,gpu_mw,ane_mw,tool_energy_impact,tool_cpu_ms_per_s

powermetrics writes one plist per sample, separated by NUL bytes.
"""
import plistlib
import sys


def main():
    if len(sys.argv) < 2:
        print(__doc__, file=sys.stderr)
        return 1
    pids = {int(p) for p in sys.argv[2:]}
    with open(sys.argv[1], "rb") as fh:
        chunks = [c.strip() for c in fh.read().split(b"\0") if c.strip()]
    print("sample,combined_mw,cpu_mw,gpu_mw,ane_mw,tool_energy_impact,tool_cpu_ms_per_s")
    for i, chunk in enumerate(chunks, start=1):
        try:
            doc = plistlib.loads(chunk)
        except Exception:  # a truncated last sample when the run is cut short
            continue
        proc = doc.get("processor", {})
        energy = 0.0
        cpu_ms = 0.0
        for task in doc.get("tasks", []):
            if task.get("pid") in pids:
                energy += float(task.get("energy_impact_per_s", 0.0))
                cpu_ms += float(task.get("cputime_ms_per_s", 0.0))
        print(
            f"{i},{proc.get('combined_power', 0):.0f},{proc.get('cpu_power', 0):.0f},"
            f"{proc.get('gpu_power', 0):.0f},{proc.get('ane_power', 0):.0f},{energy:.1f},{cpu_ms:.1f}"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
