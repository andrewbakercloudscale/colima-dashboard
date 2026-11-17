#!/usr/bin/env python3
"""Print one battery sample as CSV: percent,mah,mv,ma,draw_mw (draw positive while discharging)."""
import plistlib
import subprocess
import sys


def main():
    raw = subprocess.run(["ioreg", "-rn", "AppleSmartBattery", "-a"], capture_output=True, check=True).stdout
    d = plistlib.loads(raw)[0]
    ma = int(d.get("InstantAmperage", 0))
    if ma > 2**63:  # ioreg reports a signed value as unsigned
        ma -= 2**64
    mv = int(d.get("Voltage", 0))
    mah = d.get("AppleRawCurrentCapacity") or d.get("BatteryData", {}).get("AppleRawCurrentCapacity", "")
    print(f"{d.get('CurrentCapacity', '')},{mah},{mv},{ma},{-mv * ma / 1000:.0f}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
