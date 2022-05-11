#!/usr/bin/env bash

# Run all tests in directories named test*

# Expects compiled sender, receiver, and nanotube in corresponding dirs.
# set -e

PIDS=''

trap_pid() {
    PIDS+="$1 "
    trap '{ echo "*** force-killing $PIDS ***"; kill -9 $PIDS 2>/dev/null; exit 255; }' SIGINT SIGTERM ERR
}

for d in test* ; do
    echo -e "\n*** testing ${d} ***"
    tmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t 'tmpd') # portable for both Linux and Darwin

    echo -e "\n>>> tempdir: ${tmpdir}"

    echo -en "\n>>> starting nanotube..."
    cd "${d}/nanotube/" || exit 1
    ../../../nanotube -config="config.toml" &
    ntPID=$!
    trap_pid ${ntPID}
    echo -e "\r>>> starting nanotube: pid ${ntPID}"
    cd ../..

    if [ -e "${d}/out.tar.bz2" ] && [ ! -d "${d}/out" ]; then
        echo -e "\n>>> decompressing output"
        rm -rf "${d}/out"
        tar -C "${d}" -jxf "${d}/out.tar.bz2"
    fi

    echo -en "\n>>> starting receiver..."
    ./receiver/receiver -promPort 8024 -ports "$(ls -x "${d}/out")" -outdir "$tmpdir" &
    recPID=$!
    trap_pid ${recPID}
    echo -e "\r>>> starting receiver: pid ${recPID}"

    echo -e "\n>>> wait for receiver to start"
    while true
    do
        sleep 1;
        r=$(curl -sS localhost:8024/metrics | grep '^receiver_n_open_ports' | tr -d '\012\015' | cut -d' ' -f2)
        [[ $r -gt 0 ]] && break;
    done
    tOld=$(curl -sS localhost:8024/metrics | grep '^receiver_time_of_last_write' | tr -d '\012\015' | cut -d' ' -f2)

    if [ -e "${d}/in.bz2" ] && [ ! -f "${d}/in" ]; then
        echo -e "\n>>> decompressing input"
        rm -rf "${d}/in"
        bunzip2 "${d}/in.bz2" -c > "${d}/in"
    fi

    echo -e "\n>>> starting sender"
    ./sender/sender -data "${d}/in" -host localhost -port 2003 -rate 4000
    echo -e "\n>>> sender finished running"

    echo -e "\n>>> waiting for nanotube"
    kill $ntPID
    wait $ntPID

    echo -e "\n>>> waiting for receiver to process"

    while true; do
        sleep 1;
        t=$(curl -sS localhost:8024/metrics | grep '^receiver_time_of_last_write' | tr -d '\012\015' | cut -d' ' -f2)
        [ "$t" -eq "$tOld" ] && break;
        tOld=t
    done

    kill $recPID
    wait $recPID

    rm -f "${tmpdir}/in"

    echo -e "\n>>> sorting"
    for i in "$tmpdir"/*; do sort -o "$i" "$i"; done

    echo -e "\n>>> comparing"
    if ! diff -r "${d}/out" "$tmpdir"; then
        echo -e "\n>>> FAIL: ${tmpdir} and ${d}/out are different"
    else
        echo -e "\n>>> SUCCESS: ${tmpdir} and ${d}/out are identical"
    fi
done
