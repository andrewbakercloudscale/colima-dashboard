#!/bin/sh
# One I/O phase inside the benchmark container. usage: io.sh <phase> <dir>
# Phases: create (20000 small files in 200 dirs), read, tar, delete, seqwrite (1 GiB), seqread.
phase="$1"
dir="$2/bench"
case "$phase" in
create)
    mkdir -p "$dir" || exit 1
    i=0
    while [ $i -lt 200 ]; do
        mkdir -p "$dir/d$i" || exit 1
        j=0
        while [ $j -lt 100 ]; do
            head -c 4096 /dev/urandom >"$dir/d$i/f$j" || exit 1
            j=$((j + 1))
        done
        i=$((i + 1))
    done
    ;;
read) find "$dir" -type f -exec cat {} + >/dev/null ;;
tar) tar -cf - -C "$dir" . >/dev/null ;;
delete) rm -rf "$dir" ;;
seqwrite) dd if=/dev/zero of="$2/big.bin" bs=1M count=1024 conv=fsync 2>/dev/null ;;
seqread) dd if="$2/big.bin" of=/dev/null bs=1M 2>/dev/null && rm -f "$2/big.bin" ;;
*) echo "unknown phase $phase" >&2; exit 2 ;;
esac
