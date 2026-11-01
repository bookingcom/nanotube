#!/usr/bin/env python3
"""Real relay -> two real go-carbon processes, restart/reload and exact-point oracle.

All generated data, processes and listeners stay local. Requires Python 3 and
Go; run with --go-carbon CHECKOUT --scratch DIRECTORY. Transient files are
removed on exit. No receiver acknowledgements or fault replay are simulated.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import struct
import subprocess
import tempfile
import threading
import time
from e2e import port
import urllib.request
import urllib.parse
import re


class Proxy:
    """Count application wire bytes without decoding or changing framing."""
    def __init__(self, target):
        self.target = target
        self.bytes = 0
        self.errors = []
        self.lock = threading.Lock()
        self.listener = socket.socket()
        self.listener.bind(('127.0.0.1', 0))
        self.port = self.listener.getsockname()[1]
        self.listener.listen()
        self.listener.settimeout(.2)
        self.stopped = threading.Event()
        self.sockets = []
        self.threads = []
        self.thread = threading.Thread(target=self.accept)
        self.thread.start()

    def accept(self):
        while not self.stopped.is_set():
            try:
                conn, _ = self.listener.accept()
            except socket.timeout:
                continue
            except OSError:
                return
            thread = threading.Thread(target=self.forward, args=(conn,))
            self.threads.append(thread)
            thread.start()

    def forward(self, conn):
        try:
            with conn, socket.create_connection(('127.0.0.1', self.target), timeout=20) as out:
                out.settimeout(None)
                self.sockets.extend([conn, out])
                while True:
                    data = conn.recv(65536)
                    if not data:
                        return
                    out.sendall(data)
                    with self.lock:
                        self.bytes += len(data)
        except OSError as error:
            if not self.stopped.is_set():
                self.errors.append(str(error))

    def close(self):
        self.stopped.set()
        self.listener.close()
        self.thread.join(2)
        for s in self.sockets:
            try:
                s.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
        for thread in self.threads:
            thread.join(2)


def wait_port(p, timeout=30):
    deadline = time.monotonic()+timeout
    while time.monotonic() < deadline:
        try:
            with socket.create_connection(('127.0.0.1', p), timeout=.2):
                return
        except OSError:
            time.sleep(.05)
    raise RuntimeError(f'listener did not start on {p}')


def counter(p):
    with urllib.request.urlopen(f'http://127.0.0.1:{p}/metrics', timeout=5) as response:
        data = response.read().decode()
    return float(re.search(r'^metrics_received_tcp_total ([0-9.e+]+)', data, re.M)[1])


def wait_counter(p, want):
    deadline = time.monotonic()+60
    while time.monotonic() < deadline:
        try:
            got = counter(p)
            if got >= want:
                if got != want:
                    raise AssertionError(f'extra accepted points: {got}, expected {want}')
                return got
        except (OSError, TypeError):
            pass
        time.sleep(.1)
    raise AssertionError(f'receiver counter is {counter(p)}, expected {want}')


def check_store(http_port, names, total, base, timeout=90):
    deadline = time.monotonic()+timeout
    while True:
        seen = bytearray(total)
        digest = hashlib.sha256()
        count = 0
        for metric_id, name in enumerate(names):
            params = urllib.parse.urlencode(dict(target=name, **{'from': base-1, 'until': base+(total+len(names)-1)//len(names), 'format': 'json'}))
            with urllib.request.urlopen(f'http://127.0.0.1:{http_port}/render/?'+params, timeout=15) as response:
                answer = json.loads(response.read())
            for series in answer.get('metrics', []):
                start = series.get('startTime', series.get('start_time', series.get('StartTime')))
                step = series.get('stepTime', series.get('step_time', series.get('StepTime')))
                absent = series.get('isAbsent', series.get('is_absent', series.get('IsAbsent')))
                values = series.get('values', series.get('Values'))
                for offset, value in enumerate(values):
                    if absent[offset]:
                        continue
                    ts = start+offset*step
                    i = (int(ts)-base)*len(names)+metric_id
                    if not 0 <= i < total:
                        raise AssertionError('unexpected name/timestamp in store')
                    if seen[i]:
                        raise AssertionError(f'duplicate point {i}')
                    if struct.pack('!d', value) != struct.pack('!d', float(i % 8192)/8):
                        raise AssertionError(f'changed value at point {i}: {value!r}')
                    seen[i] = 1
                    count += 1
                    digest.update(name.encode()+b'\0'+struct.pack('!dq', value, int(ts)))
        if count == total:
            return count, digest.hexdigest()
        if time.monotonic() >= deadline:
            raise AssertionError(f'missing points: received {count}, expected {total}')
        time.sleep(.2)


def expected_checksum(names, total, base):
    digest = hashlib.sha256()
    for metric_id, name in enumerate(names):
        for i in range(metric_id, total, len(names)):
            digest.update(name.encode()+b'\0'+struct.pack('!dq', float(i % 8192)/8, base+i//len(names)))
    return digest.hexdigest()


def run_case(root, carbon_binary, relay_binary, mode, total, base):
    root.mkdir()
    processes, groups, logs, proxies = [], [], [], []
    carbon_ports, http_ports, prom_ports, configs = [], [], [], []
    old_counts = [0, 0]
    names = ['example.'+'application.component.worker.region.zone.'*5+f'metric_{i:04d}' for i in range(256)]
    try:
        for index in range(2):
            d = root/str(index)
            d.mkdir()
            (d/'data').mkdir()
            (d/'dump').mkdir()
            carbon_ports.append(port())
            http_ports.append(port())
            prom_ports.append(port())
            (d/'schemas.conf').write_text('[all]\npattern = .*\nretentions = 1s:6h\n')
            config = d/'carbon.conf'
            # A relative dump path keeps Unix socket names short on all platforms.
            config.write_text(f'''[common]
user = ""
metric-endpoint = ""
metric-interval = "100ms"
[[logging]]
logger = ""
level = "info"
file = "stderr"
[[logging]]
logger = "stat"
level = "error"
file = "stderr"
[cache]
max-size = 10000000
[whisper]
enabled = true
compressed = true
out-of-order = true
sparse-create = true
schemas-file = "{d/'schemas.conf'}"
data-dir = "{d/'data'}"
workers = 2
[tags]
enabled = false
[udp]
enabled = false
[tcp]
enabled = true
listen = "127.0.0.1:{carbon_ports[index]}"
[pickle]
enabled = false
[grpc]
enabled = false
[carbonserver]
enabled = true
listen = "127.0.0.1:{http_ports[index]}"
scan-frequency = "1s"
trie-index = true
concurrent-index = true
realtime-index = 1000
file-list-cache = "{d/'flc'}"
file-list-cache-version = 2
query-cache-enabled = false
find-cache-enabled = false
[carbonlink]
enabled = false
[prometheus]
enabled = true
[pprof]
enabled = false
listen = "127.0.0.1:{prom_ports[index]}"
[dump]
enabled = true
path = "dump"
restore-per-second = 0
handoff-supervisor = true
''')
            configs.append(config)

        def start_carbon(index):
            log = (root/f'carbon-{index}-{len(processes)}.log').open('w')
            logs.append(log)
            p = subprocess.Popen([str(carbon_binary), '-config', str(configs[index])], cwd=root/str(index), stdout=log, stderr=log, start_new_session=True)
            processes.append(p)
            groups.append(p.pid)
            print(f"LOCAL_PROCESS_GROUP {p.pid}", flush=True)
            wait_port(prom_ports[index])
            wait_port(carbon_ports[index])
            return p

        carbons = [start_carbon(i) for i in range(2)]
        proxies = [Proxy(p) for p in carbon_ports]
        incoming, prom = port(), port()
        clusters = root/'clusters.toml'
        clusters.write_text(f'''[[cluster]]
name = "stores"
type = "toall"
[[cluster.hosts]]
name = "127.0.0.1"
port = {proxies[0].port}
stream = true
[cluster.hosts.stream_options]
zstd = true
flush_period_ms = 25
[[cluster.hosts]]
name = "127.0.0.1"
port = {proxies[1].port}
compression = "{'zstd' if mode == 'zstd' else 'none'}"
''')
        rules = root/'rules.toml'
        rules.write_text('[[rule]]\nregexs = [".*"]\nclusters = ["stores"]\n')
        config = root/'nanotube.toml'
        config.write_text(f'''ClustersConfig = "{clusters}"
RulesConfig = "{rules}"
ListenTCP = "127.0.0.1:{incoming}"
PromPort = {prom}
PromLocal = true
PprofPort = -1
TermTimeoutSec = 30
IncomingConnIdleTimeoutSec = 3600
MainQueueSize = {total+65536}
MainQueueBatchSize = 0
WorkerPoolSize = 2
HostQueueSize = {total+65536}
TCPOutCompression = "zstd"
TCPOutBufSize = 1048576
TCPOutBufFlushPeriodSec = 1
TCPOutConnectionRefreshPeriodSec = 0
SendTimeoutSec = 30
''')
        relay_log_path = root/'nanotube.log'
        relay_log = relay_log_path.open('w')
        logs.append(relay_log)
        relay = subprocess.Popen([str(relay_binary), '-config', str(config)], stdout=relay_log, stderr=relay_log, start_new_session=True)
        processes.append(relay)
        groups.append(relay.pid)
        print(f"LOCAL_PROCESS_GROUP {relay.pid}", flush=True)
        wait_port(prom)
        producer = socket.create_connection(('127.0.0.1', incoming), timeout=30)
        restart_errors = []
        restart = None

        def restart_carbon():
            try:
                for index in range(2):
                    carbons[index].send_signal(signal.SIGUSR2)
                    carbons[index].wait(timeout=60)
                    carbons[index] = start_carbon(index)
            except BaseException as error:
                restart_errors.append(error)

        plain_bytes = 0
        reloaded = False
        for start in range(0, total, 4096):
            data = ''.join(f'{names[i % len(names)]} {float(i % 8192)/8:.17g} {base+i//len(names)}\n' for i in range(start, min(start+4096,total))).encode()
            producer.sendall(data)
            time.sleep(.005)  # Bound generator bursts independently of relay throughput.
            plain_bytes += len(data)
            if start >= total//3 and restart is None:
                old_counts[:] = [wait_counter(p, min(start+4096, total)) for p in prom_ports]
                time.sleep(2)  # Allow the filesystem index snapshot to become handoff-ready.
                # Keep the sender connection open; the receiver transfers its
                # live dictionary and blocked socket read to the successor.
                restart = threading.Thread(target=restart_carbon)
                restart.start()
                restart.join(65)
                if restart.is_alive() or restart_errors:
                    raise RuntimeError(f'carbon restart failed: {restart_errors}')
            if start >= total*2//3 and not reloaded:
                restart.join(65)
                if restart.is_alive() or restart_errors:
                    raise RuntimeError(f'carbon restart failed: {restart_errors}')
                relay.send_signal(signal.SIGUSR2)
                producer.close()
                relay.wait(timeout=60)
                # USR2 starts a replacement which owns the inherited listener.
                entries = [json.loads(line) for line in relay_log_path.read_text().splitlines() if line.startswith('{')]
                child = [int(e['pid']) for e in entries if e.get('msg', '').endswith('Started new process. Moved FDs.')]
                if not child:
                    raise AssertionError('reload did not start a replacement')
                wait_port(prom)
                producer = socket.create_connection(('127.0.0.1', incoming), timeout=30)
                reloaded = True
        producer.close()
        if restart:
            restart.join(65)
        if restart_errors:
            raise RuntimeError(restart_errors)
        accepted = [old_counts[i]+wait_counter(p, total-old_counts[i]) for i,p in enumerate(prom_ports)]
        results = [check_store(p, names, total, base) for p in http_ports]
        if any(proxy.errors for proxy in proxies):
            raise AssertionError(f'proxy transport failed: {[p.errors for p in proxies]}')
        if [old_counts[i]+counter(p) for i,p in enumerate(prom_ports)] != [total,total]:
            raise AssertionError('late duplicate delivery after storage verification')
        expected = expected_checksum(names, total, base)
        if any(digest != expected for _, digest in results):
            raise AssertionError('store checksum differs from source checksum')
        for index in range(2):
            carbon_logs = '\n'.join(p.read_text() for p in root.glob(f'carbon-{index}-*.log'))
            if 'took over input from stopping instance' not in carbon_logs:
                raise AssertionError(f'store {index} did not inherit accepted input sockets')
        if mode == 'plaintext' and proxies[1].bytes != plain_bytes:
            raise AssertionError('plaintext wire bytes differ from the exact source input')
        result = dict(mode=mode, points_per_store=total, accepted=accepted, checksum=expected, stores=results,
                      wire_bytes=dict(stream=proxies[0].bytes, other=proxies[1].bytes),
                      wire_bytes_per_point=dict(stream=proxies[0].bytes/total, other=proxies[1].bytes/total),
                      plaintext_input_bytes=plain_bytes, carbon_handoff=True, nanotube_usr2=True)
        print(json.dumps(result), flush=True)
        return result
    except BaseException:
        try:
            with urllib.request.urlopen(f'http://127.0.0.1:{prom}/metrics', timeout=3) as response:
                metrics = response.read().decode()
            print('RELAY_DIAGNOSTICS', '\n'.join(line for line in metrics.splitlines() if not line.startswith('#') and any(key in line for key in ['dropped', 'thrott', 'in_records_total', 'pending_records', 'flushed_records'])), flush=True)
        except Exception:
            pass
        for p in root.glob('*.log'):
            print(p.name, p.read_text()[-18000:], flush=True)
        raise
    finally:
        for group in groups:
            try:
                os.killpg(group, signal.SIGKILL)
            except ProcessLookupError:
                pass
        for p in processes:
            try:
                p.wait(timeout=10)
            except subprocess.TimeoutExpired:
                pass
        for proxy in proxies:
            proxy.close()
        for log in logs:
            log.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--go-carbon', type=Path, required=True)
    parser.add_argument('--scratch', type=Path, required=True)
    parser.add_argument('--points', type=int, default=3000000)
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[2]
    source = args.go_carbon.resolve()
    with tempfile.TemporaryDirectory(prefix='e2e-', dir=args.scratch) as temporary:
        root = Path(temporary)
        env = dict(os.environ, TMPDIR=str(root), GOTOOLCHAIN='go1.27.0')
        carbon, relay = root/'go-carbon', root/'nanotube'
        subprocess.run(['go','build','-o',str(carbon),'.'],cwd=source,env=dict(env,GOFLAGS='-mod=vendor'),check=True)
        subprocess.run(['go','build','-o',str(relay),'./cmd/nanotube'],cwd=repo,env=env,check=True)
        base = int(time.time())-(args.points+255)//256-60
        for mode in ['plaintext', 'zstd']:
            run_case(root/mode, carbon, relay, mode, args.points, base)


if __name__ == '__main__':
    main()
