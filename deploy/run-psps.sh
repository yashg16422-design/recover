#!/usr/bin/env bash
# Runs the three mock payment gateways (same personalities as `make psps`) bound to localhost only.
set -euo pipefail
cd /opt/recover
trap 'kill $(jobs -p) 2>/dev/null || true' EXIT INT TERM
./psp -addr 127.0.0.1:9001 -name psp-a -success 0.90 -latency 40  -cost 1.0 &
./psp -addr 127.0.0.1:9002 -name psp-b -success 0.98 -latency 90  -cost 2.0 &
./psp -addr 127.0.0.1:9003 -name psp-c -success 0.95 -latency 150 -cost 1.5 &
wait
