#!/usr/bin/env bash

# Run all tests in directories named test*

# Expects compiled sender, receiver, and nanotube in corresponding dirs.
# set -e

for d in test*/ ; do
    tmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t 'tmpd') # portable for both Linux and Darwin
    trap '{ rm -r $tmpdir; }' EXIT
    ../nanotube -clusters "$d/nanotube/clusters.toml" -config "$d/nanotube/config.toml" -rules "$d/nanotube/rules.toml" &
    ntPID=$!

    ./receiver/receiver -ports "$(ls -x "$d/out")" -outdir "$tmpdir" &
    recPID=$!
    trap '{ kill $recPID; }' EXIT

    # wait for receiver to start
    sleep 1
    ./sender/sender -data "$d/in" -host localhost -port 2003

    # wait for records to propagate through receiver
    sleep 1
    kill $ntPID
    wait $ntPID

    if ! diff -qr "${d}out" "$tmpdir"; then
        exit 1
    fi
done

