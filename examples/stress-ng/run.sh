#!/bin/sh
# Runs every stress-ng scenario once, so each thing Colima Dashboard should raise appears:
# open the dashboard's General page while it runs. Takes about 5 minutes; stops by itself.
set -u
cd "$(dirname "$0")"
dc() { docker compose "$@"; }

dc --profile all build -q
echo "1. CPU hog on every VM CPU for 5 minutes (High CPU in the VM, after about a minute)"
dc up -d cpu-hog io-hog noisy-logger
echo "2. Out of memory test (stopped by the kernel)"
dc run oom-test || echo "   oom-test exited $? (expected)"
echo "3. Failing performance test"
dc run perf-check || echo "   perf-check exited $? (expected)"
echo
echo "Now look at the dashboard: General (things to fix), Monitor, Runs and tests, Errors."
echo "Stop early with: docker compose --profile all down"
