#!/usr/bin/env python3
"""Localhost integration test. Requires a stream-capable go-carbon checkout.

python3 test/stream/e2e.py --go-carbon ../gc-stream --scratch /path/to/scratch
All processes, listeners, generated names, and configuration stay local.
"""
import argparse
import collections
import json
import os
from pathlib import Path
import pickle
import socket
import struct
import subprocess
import tempfile
import time


def port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def receive(s, n):
    data = bytearray()
    while len(data) < n:
        b = s.recv(n-len(data))
        if not b:
            raise RuntimeError("unexpected carbonlink EOF")
        data.extend(b)
    return data


def query(p, name):
    request = pickle.dumps({"type": "cache-query", "metric": name}, protocol=2)
    with socket.create_connection(("127.0.0.1", p), timeout=5) as s:
        s.sendall(struct.pack("!I", len(request))+request)
        size, = struct.unpack("!I", receive(s, 4))
        return pickle.loads(receive(s, size), encoding="bytes")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go-carbon", type=Path, required=True)
    parser.add_argument("--scratch", type=Path, required=True)
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[2]
    source = args.go_carbon.resolve()
    args.scratch.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="stream-e2e-", dir=args.scratch) as root:
        root = Path(root)
        env = dict(os.environ, TMPDIR=str(root))
        carbon = root/"go-carbon"
        sender = root/"stream-sender"
        subprocess.run(["go", "build", "-o", str(carbon), "."], cwd=source, env=env, check=True)
        subprocess.run(["go", "build", "-o", str(sender), "./test/stream"], cwd=repo, env=env, check=True)
        for multi in (1, 2, 10):
            for zstd in (False, True):
                stream_port, cache_port = port(), port()
                config = root/"go-carbon.conf"
                config.write_text(f'''[common]
user = ""
metric-endpoint = ""
[whisper]
enabled = false
[udp]
enabled = false
[tcp]
enabled = true
listen = "127.0.0.1:{stream_port}"
[pickle]
enabled = false
[grpc]
enabled = false
[carbonserver]
enabled = false
[carbonlink]
enabled = true
listen = "127.0.0.1:{cache_port}"
''')
                with (root/"carbon.log").open("w+") as log:
                    proc = subprocess.Popen([str(carbon), "-config", str(config)], env=env, stdout=log, stderr=log)
                    try:
                        deadline = time.monotonic()+20
                        while True:
                            try:
                                with socket.create_connection(("127.0.0.1", stream_port), timeout=.2):
                                    break
                            except OSError:
                                if proc.poll() is not None or time.monotonic() > deadline:
                                    log.seek(0)
                                    raise RuntimeError("go-carbon did not start: "+log.read())
                                time.sleep(.05)
                        command = [str(sender), "-port", str(stream_port), "-mtcp", str(multi)]
                        if zstd:
                            command.append("-zstd")
                        result = subprocess.run(command, env=env, capture_output=True, text=True, check=True, timeout=30)
                        expected = json.loads(result.stdout)
                        want = collections.defaultdict(collections.Counter)
                        for p in expected:
                            want[p["name"]][(p["timestamp"], p["bits"])] += 1
                        deadline = time.monotonic()+10
                        while True:
                            got = {}
                            for name in want:
                                answer = query(cache_port, name)
                                points = answer.get("datapoints", answer.get(b"datapoints", []))
                                got[name] = collections.Counter((int(ts), struct.pack("!d", value).hex()) for ts, value in points)
                            if got == want:
                                break
                            if time.monotonic() > deadline:
                                raise AssertionError("cache points differ: "+str({n: (sum(want[n].values()), sum(got[n].values())) for n in want if want[n] != got[n]}))
                            time.sleep(.05)
                        rejected = query(cache_port, "example.rejected")
                        assert not rejected.get("datapoints", rejected.get(b"datapoints", []))
                        print(f"PASS mtcp={multi} zstd={zstd}: {len(expected)} exact points through idle reconnects and dictionary resets")
                    finally:
                        proc.terminate()
                        try:
                            proc.wait(timeout=10)
                        except subprocess.TimeoutExpired:
                            proc.kill()
                            proc.wait()


if __name__ == "__main__":
    main()
