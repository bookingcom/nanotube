#!/usr/bin/env bash

# Run all tests in directories named test*

# Expects compiled sender, receiver, and nanotube in corresponding dirs.
# set -e

PIDS=''
trap_pid() {
    PIDS+="$1 "
    trap '{ echo "*** force-killing $PIDS ***"; kill -9 $PIDS 2>/dev/null; exit 255; }' SIGINT SIGTERM ERR SIGKILL
}

for d in test* ; do
    echo -e "\n*** testing ${d} ***"
    tmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t 'tmpd') # portable for both Linux and Darwin

    echo -e "\n. tempdir: ${tmpdir}"

    echo -en "\n. starting nanotube..."
    cd "${d}/nanotube/"
    ../../../nanotube -config="config.toml" &
    ntPID=$!
    trap_pid ${ntPID}
    echo -e "\r. starting nanotube: pid ${ntPID}"
    cd ../..

    echo -en "\n. starting receiver..."
    ./receiver/receiver -ports "$(ls -x "${d}/out")" -outdir "$tmpdir" &
    recPID=$!
    trap_pid ${recPID}
    echo -e "\r. starting receiver: pid ${recPID}"

    # wait for receiver to start
    sleep 1
    if [ -e ${d}/in.bz2 ] && [ ! -f ${d}/in ]; then
        echo -e "\n. decompressing input"
        rm -rf ${d}/in
        bunzip2 ${d}/in.bz2 -c > ${d}/in
    fi

    if [ -e ${d}/out.tar.bz2 ] && [ ! -d ${d}/out ]; then
        echo -e "\n. decompressing output"
        rm -rf ${d}/out
        tar -C ${d} -jxf ${d}/out.tar.bz2
    fi

    echo -e "\n. starting sender"
    ./sender/sender -data "${d}/in" -host localhost -port 2003

    # wait for records to propagate through receiver
    sleep 5

    kill $ntPID
    wait $ntPID

    sleep 5

    kill $recPID
    wait $recPID
    
    rm -f ${tmpdir}/in

    echo -e "\n. sorting"
    for i in $tmpdir/*; do sort -o $i $i; done

    echo -e "\n. comparing"
    if ! diff -qr "${d}/out" "$tmpdir"; then
        echo "   -> FAIL: ${tmpdir} and ${d}/out are different"
    else
        echo "   -> SUCCESS: ${tmpdir} and ${d}/out are identical"
    fi
done
