#!/usr/bin/env bash

# Run all tests in directories named test*

# Expects compiled sender, receiver, and nanotube in corresponding dirs.
# set -e

PIDS=''
JQ_BIN='jq'

trap_pid() {
    PIDS+="$1 "
    trap '{ echo "*** force-killing $PIDS ***"; kill -9 $PIDS 2>/dev/null; exit 255; }' SIGINT SIGTERM
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
    ./receiver/receiver -local-api-port 8024 -ports "$(ls -x "${d}/out")" -outdir "$tmpdir" &
    recPID=$!
    trap_pid ${recPID}
    echo -e "\r>>> starting receiver: pid ${recPID}"

    echo -e "\n>>> wait for receiver to start"
    while true
    do
        sleep 1;
        r=$(curl -sS localhost:8024/status | ${JQ_BIN} .Ready)
        [[ $r = "true" ]] && break;
    done

    if [ -e "${d}/in.bz2" ] && [ ! -f "${d}/in" ]; then
        echo -e "\n>>> decompressing input"
        rm -rf "${d}/in"
        bunzip2 "${d}/in.bz2" -c > "${d}/in"
    fi

    echo -e "\n>>> starting sender"
    ./sender/sender -data "${d}/in" -host localhost -port 2003 -rate 4000
    echo -e "\n>>> sender finished running"

    echo -e "\n>>> checking Nanotube for throttling"
    nTh=$(curl -sS localhost:9090/metrics | grep '^nanotube_throttled_records_total' | tr -d '\012\015' | cut -d' ' -f2)
    if [ "$nTh" -ne 0 ]; then
        echo -e "\n>>> got throttled main q records"
        exit 1
    fi
    nThHosts=$(curl -sS localhost:9090/metrics | grep '^nanotube_throttled_host_records_total' | tr -d '\012\015' | cut -d' ' -f2)
    if [ "$nThHosts" -ne 0 ]; then
        echo -e "\n>>> got throttled hosts records"
        exit 1
    fi

    echo -e "\n>>> terminating Nanotube"
    kill -TERM $ntPID
    echo -e "\n>>> waiting for nanotube"
    wait $ntPID

    echo -e "\n>>> waiting for receiver to process"

    while true; do
        sleep 1;
        t=$(curl -sS localhost:8024/status | ${JQ_BIN} .IdleTimeMilliSecs);
        (( "$t" > 2000 )) && break;
    done

    kill -TERM $recPID
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
