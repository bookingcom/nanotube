#!/usr/bin/env python3
"""Local-only matched listener model; run a prebuilt cmd/nanotube test binary.

Generate fixtures with TestStreamAdmissionFixtures into an explicit scratch
directory first. No fixture generation or client socket cost enters relay CPU.
The relay uses its real listener/decoder/router and a validating in-memory sink.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--fixtures", type=Path)
    parser.add_argument("--mode", choices=("plain", "raw", "zstd", "idle"), required=True)
    parser.add_argument("--clients", type=int, default=1000)
    parser.add_argument("--names", type=int, default=2000)
    parser.add_argument("--groups", type=int, default=50)
    parser.add_argument("--rounds", type=int, default=50)
    parser.add_argument("--sparse-seconds", type=int, default=0)
    parser.add_argument("--total", type=int, default=16 << 30)
    parser.add_argument("--source-port", type=int, default=0,
                        help="start explicit local ports for >16k sockets on macOS")
    args = parser.parse_args()
    env = dict(os.environ, STREAM_ADMISSION_MODEL="server", STREAM_ADMISSION_TOTAL=str(args.total))
    relay = subprocess.Popen(
        [args.binary, "-test.run=^TestStreamAdmissionProcess$", "-test.timeout=0"],
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, env=env,
    )
    clients = []

    def receive():
        while line := relay.stdout.readline():
            if line.startswith("{"):
                return json.loads(line)
        raise RuntimeError(f"relay exited {relay.poll()}")

    def command(text):
        relay.stdin.write(text + "\n")
        relay.stdin.flush()
        return receive()

    def send_all(payloads, executor):
        def stripe(worker):
            for i in range(worker, len(clients), 8):
                clients[i].sendall(payloads[i % len(payloads)])
        list(executor.map(stripe, range(8)))

    try:
        address = receive()["address"].rsplit(":", 1)
        baseline = command("memory")
        flags = 1 if args.mode in ("zstd", "idle") else 0
        if args.mode == "idle":
            prime = [b"\0GCS\r\n\x1a\n\x01\x01"]
            warm = []
        else:
            suffix = "plain" if args.mode == "plain" else "prime"
            warm_suffix = "plainwarm" if args.mode == "plain" else "warm"
            prime = [(args.fixtures / f"{i}-{flags}-{suffix}").read_bytes() for i in range(args.groups)]
            warm = [[(args.fixtures / f"{i}-{flags}-{warm_suffix}{epoch}").read_bytes()
                     for i in range(args.groups)] for epoch in range(2)]
        setup_start = time.monotonic()
        source_port = args.source_port
        for _ in range(args.clients):
            if source_port:
                conn = socket.socket()
                conn.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                conn.settimeout(90)
                while True:
                    try:
                        conn.bind(("127.0.0.1", source_port))
                        source_port += 1
                        break
                    except OSError:
                        source_port += 1
                        if source_port > 65000:
                            raise
                conn.connect((address[0], int(address[1])))
            else:
                conn = socket.create_connection((address[0], int(address[1])), timeout=90)
            clients.append(conn)
        connected = command("sample")
        with ThreadPoolExecutor(max_workers=8) as executor:
            send_all(prime, executor)
            expected = 0 if args.mode == "idle" else args.names * args.clients
            if args.mode == "idle":
                deadline = time.monotonic() + 20
                while True:
                    primed = command("sample")
                    if primed["active"] + primed["errors"] >= args.clients:
                        break
                    if time.monotonic() > deadline:
                        raise RuntimeError("idle admission stalled")
                    time.sleep(0.01)
            else:
                primed = command(f"wait {expected}")
                if primed["records"] != expected or primed["errors"]:
                    raise RuntimeError(f"priming failed: {primed}")
            setup_seconds = time.monotonic() - setup_start
            retained = command("memory")
            if args.mode in ("raw", "zstd") and retained["shared_names"] != args.names * min(args.groups, args.clients):
                raise RuntimeError(f"fixture did not retain its full name set: {retained}")
            before = command("sample")
            warm_start = time.monotonic()
            if args.mode != "idle":
                if args.sparse_seconds:
                    due = [warm_start + (i % 101) / 101 for i in range(args.clients)]
                    epochs = [0] * args.clients
                    while time.monotonic() < warm_start + args.sparse_seconds:
                        now = time.monotonic()
                        for i, conn in enumerate(clients):
                            if due[i] <= now:
                                conn.sendall(warm[epochs[i] % 2][i % args.groups])
                                epochs[i] += 1
                                expected += 64
                                due[i] += 10 + i % 51
                        time.sleep(0.01)
                else:
                    for epoch in range(args.rounds):
                        send_all(warm[epoch % 2], executor)
                        expected += args.clients * 64
            after = command(f"wait {expected}")
            warm_seconds = time.monotonic() - warm_start
        if after["records"] != expected or after["errors"]:
            raise RuntimeError(f"warm delivery failed: {after}")
        for conn in clients:
            conn.close()
        clients.clear()
        deadline = time.monotonic() + 20
        while True:
            closed = command("memory")
            if closed["tcp_active"] == 0:
                break
            if time.monotonic() > deadline:
                raise RuntimeError("connections did not drain")
            time.sleep(0.01)
        records = after["records"] - before["records"]
        if closed["reserved"] != baseline["reserved"] or closed["shared_names"] or closed["routes"]:
            raise RuntimeError(f"closed inputs leaked admission or shared state: {closed}")
        print(json.dumps({
            "parameters": vars(args) | {"fixtures": str(args.fixtures)},
            "environment": {key: os.environ.get(key, "") for key in ("GOMAXPROCS", "GOMEMLIMIT", "GOGC")},
            "baseline": baseline, "connected": connected, "primed": primed,
            "retained": retained, "before": before, "after": after, "closed": closed,
            "setup_seconds": setup_seconds, "warm_seconds": warm_seconds,
            "setup_cpu_ns_per_connection": (primed["cpu_ns"] - baseline["cpu_ns"]) / args.clients,
            "warm_cpu_ns_per_record": (after["cpu_ns"] - before["cpu_ns"]) / records if records else None,
            "wire_prime_bytes_per_client": sum(map(len, prime)) / len(prime),
            "wire_warm_bytes_per_record": sum(len(p) for epoch in warm for p in epoch) / (2 * args.groups * 64) if warm else None,
        }, sort_keys=True), flush=True)
    except Exception as error:
        diagnostic = {"failure": str(error)}
        for key in ("baseline", "connected", "primed", "retained", "before"):
            if key in locals():
                diagnostic[key] = locals()[key]
        try:
            diagnostic["failure_sample"] = command("sample")
        except Exception:
            pass
        print(json.dumps(diagnostic, sort_keys=True), file=sys.stderr, flush=True)
        raise
    finally:
        for conn in clients:
            conn.close()
        if relay.poll() is None:
            relay.stdin.write("stop\n")
            relay.stdin.flush()
            relay.wait(timeout=30)
        if relay.returncode:
            raise RuntimeError(f"relay exit status {relay.returncode}")


if __name__ == "__main__":
    main()
