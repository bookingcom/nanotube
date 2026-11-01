#!/usr/bin/env python3
"""Local client -> three sticky relays -> two real stores, with point accounting.

Each failure interrupts a live producer. A successful TCP write has no receipt
acknowledgement: lost and duplicate accepted points are reported, never hidden.
Supply prebuilt local binaries; all generated data lives under --scratch.
"""
import argparse
import json
import os
from pathlib import Path
import re
import signal
import socket
import struct
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

from e2e import port
from restart_e2e import carbon_config, counter, wait_counter, wait_port


def metric(prom, name):
    with urllib.request.urlopen(f'http://127.0.0.1:{prom}/metrics', timeout=5) as r:
        return sum(float(line.rsplit(' ', 1)[1]) for line in r.read().decode().splitlines()
                   if line.startswith(name+' ') or line.startswith(name+'{'))


class Process:
    def __init__(self, root, binary, config, incoming, prom):
        self.root, self.binary, self.config = root, binary, config
        self.incoming, self.prom = incoming, prom
        self.groups, self.children, self.logs = [], [], []
        self.old_count = 0
        self.start()

    def start(self):
        log = (self.root/f'process-{len(self.children)}.log').open('w')
        self.logs.append(log)
        self.process = subprocess.Popen([str(self.binary), '-config', str(self.config)],
                                        cwd=self.root, stdout=log, stderr=log,
                                        start_new_session=True)
        self.groups.append(self.process.pid)
        self.children.append(self.process)
        wait_port(self.prom)
        wait_port(self.incoming)

    def usr2(self):
        self.process.send_signal(signal.SIGUSR2)
        self.process.wait(timeout=60)
        if self.process.returncode:
            raise AssertionError(f'reload exit {self.process.returncode}')
        # nanotube spawns a successor; go-carbon supervisor hands off to a
        # separately started process and retains its live input sockets.
        if self.binary.name == 'go-carbon':
            self.start()
        else:
            deadline = time.monotonic()+30
            while time.monotonic() < deadline:
                text = '\n'.join(p.read_text() for p in self.root.glob('process-*.log'))
                entries = [json.loads(line) for line in text.splitlines() if line.startswith('{')]
                ids = [int(e['pid']) for e in entries
                       if e.get('msg', '').endswith('Started new process. Moved FDs.')]
                if ids:
                    self.successor = ids[-1]
                    wait_port(self.prom)
                    return
                time.sleep(.05)
            raise AssertionError('replacement PID missing')

    def close(self):
        for group in self.groups:
            try:
                os.killpg(group, signal.SIGKILL)
            except ProcessLookupError:
                pass
        for p in self.children:
            p.wait(timeout=10)
        for log in self.logs:
            log.close()


def observe_store(http, names, total, base):
    seen = bytearray(total)
    wrong = 0
    indices = {name: i for i, name in enumerate(names)}
    target = names[0].rsplit('_', 1)[0]+'_*'
    q = urllib.parse.urlencode({'target': target, 'from': base-1,
                                'until': base+(total+len(names)-1)//len(names)+1,
                                'format': 'json'})
    try:
        with urllib.request.urlopen(f'http://127.0.0.1:{http}/render?{q}', timeout=15) as r:
            answer = json.load(r)
    except urllib.error.HTTPError as e:
        if e.code != 404:
            raise
        answer = {}
    for series in answer.get('metrics', []):
        index = indices[series['name']]
        start = series.get('startTime', series.get('start_time', series.get('StartTime')))
        step = series.get('stepTime', series.get('step_time', series.get('StepTime')))
        absent = series.get('isAbsent', series.get('is_absent', series.get('IsAbsent')))
        values = series.get('values', series.get('Values'))
        for off, value in enumerate(values):
            if absent[off]:
                continue
            i = (int(start+off*step)-base)*len(names)+index
            if not 0 <= i < total:
                raise AssertionError('unexpected name/timestamp')
            if struct.pack('!d', value) != struct.pack('!d', float(i % 8192)/8):
                wrong += 1
            seen[i] = 1
    return {'unique': sum(seen), 'missing': total-sum(seen), 'wrong_values': wrong}


def run(root, nt, carbon, mode, event, total):
    root.mkdir()
    processes, stores, relays = [], [], []
    names = ['example.'+'application.component.worker.region.zone.'*5+f'metric_{i:04d}'
             for i in range(128)]
    base = int(time.time())-(total+127)//128-120
    try:
        for i in range(2):
            d = root/f'store{i}'
            d.mkdir()
            (d/'data').mkdir()
            (d/'dump').mkdir()
            (d/'schemas.conf').write_text("[all]\npattern=.*\nretentions=1s:6h\n")
            incoming, http, prom = port(), port(), port()
            config = d/'config'
            config.write_text(carbon_config(d, incoming, http, prom))
            p = Process(d, carbon, config, incoming, prom)
            p.http = http
            stores.append(p)
            processes.append(p)

        def nanotube(name, hosts, kind, stream):
            d = root/name
            d.mkdir()
            incoming, prom = port(), port()
            cluster = ['[[cluster]]', 'name="dest"', f'type="{kind}"',
                       f'stream={str(stream).lower()}', '[cluster.stream_options]',
                       'zstd=true', 'flush_period_ms=25']
            for p in hosts:
                cluster += ['[[cluster.hosts]]', 'name="127.0.0.1"', f'port={p.incoming}']
            (d/'clusters').write_text('\n'.join(cluster)+'\n')
            (d/'rules').write_text('[[rule]]\nprefixes=["example."]\nclusters=["dest"]\n')
            cfg = d/'config'
            cfg.write_text(f'''ClustersConfig="{d/'clusters'}"
RulesConfig="{d/'rules'}"
ListenTCP="127.0.0.1:{incoming}"
PromPort={prom}
PromLocal=true
PprofPort=-1
TermTimeoutSec=30
IncomingConnIdleTimeoutSec=3600
MainQueueSize=8192
MainQueueBatchSize=256
BatchFlushPerdiodSec=1
HostQueueSize={total+65536}
WorkerPoolSize=2
TCPOutBufSize=65536
TCPOutBufFlushPeriodSec=1
TCPOutConnectionRefreshPeriodSec=0
SendTimeoutSec=5
RouteCacheBytes=0
''')
            p = Process(d, nt, cfg, incoming, prom)
            processes.append(p)
            return p

        for i in range(3):
            relays.append(nanotube(f'relay{i}', stores, 'toall', True))
        client = nanotube('client', relays, 'sticky', mode == 'stream')
        time.sleep(.3)
        ready = threading.Event()
        resume = threading.Event()
        boundary = [0]
        reconnects = [0]
        errors = []

        def produce():
            conn = None
            try:
                for first in range(0, total, 512):
                    data = ''.join(f'{names[i%len(names)]} {float(i%8192)/8:.17g} {base+i//len(names)}\n'
                                   for i in range(first, min(first+512, total))).encode()
                    while True:
                        try:
                            if conn is None:
                                conn = socket.create_connection(('127.0.0.1', client.incoming), timeout=5)
                            conn.sendall(data)
                            break
                        except OSError:
                            if conn:
                                conn.close()
                            conn = None
                            reconnects[0] += 1
                            if reconnects[0] > 100:
                                raise
                            time.sleep(.05)
                    if first >= total//3 and not ready.is_set():
                        boundary[0] = min(first+512, total)
                        ready.set()
                        if event == "store-handoff" and not resume.wait(90):
                            raise AssertionError("handoff did not resume producer")
                    time.sleep(.002)
            except BaseException as error:
                errors.append(repr(error))
            finally:
                if conn:
                    conn.close()
                ready.set()

        producer = threading.Thread(target=produce)
        producer.start()
        if not ready.wait(30):
            raise AssertionError('producer stalled')
        before_counts = [counter(s.prom) for s in stores]
        if event == 'relay-usr2':
            relays[0].usr2()
        elif event == 'relay-kill':
            relays[0].process.kill()
            relays[0].process.wait(timeout=10)
            time.sleep(.5)
            relays[0].start()
        elif event == 'client-restart':
            client.process.send_signal(signal.SIGTERM)
            client.process.wait(timeout=60)
            client.start()
        elif event == 'store-handoff':
            # Fence source acceptance to obtain exact pre/post receiver counters;
            # keep every relay socket and its dictionary open across handoff.
            for s in stores:
                s.old_count = wait_counter(s.prom, boundary[0])
            time.sleep(2)
            for s in stores:
                s.usr2()
            resume.set()
        producer.join(60)
        if producer.is_alive() or errors:
            raise AssertionError(f'producer failed: {errors}')
        # Flush every sender and wait for all counters to stop changing.
        last, stable = None, 0
        for _ in range(100):
            counts = [counter(s.prom) for s in stores]
            if counts == last:
                stable += 1
            else:
                stable = 0
            last = counts
            if stable >= 15:
                break
            time.sleep(.2)
        # The HTTP field oracle is independent of transport counters. Counters
        # additionally expose duplicates that a last-write-wins store masks.
        details = []
        for s in stores:
            # Concurrent cache eviction/persistence can make an immediate render
            # incomplete. Wait for a stable final field oracle, independently of
            # the acceptance counter, before classifying any missing points.
            deadline, previous, stable = time.monotonic()+90, None, 0
            while True:
                result = observe_store(s.http, names, total, base)
                stable = stable+1 if result == previous else 0
                if result['missing'] == 0 or stable >= 5 or time.monotonic() > deadline:
                    break
                previous = result
                time.sleep(1)
            result['accepted_current_process'] = counter(s.prom)
            result['accepted'] = s.old_count + result['accepted_current_process']
            result['duplicate_accepts'] = result['accepted']-result['unique']
            details.append(result)
        logs = '\n'.join(p.read_text() for proc in processes for p in proc.root.glob('process-*.log'))
        if event in ('healthy', 'store-handoff') and any(d['missing'] or d['duplicate_accepts'] for d in details):
            raise AssertionError(details)
        if any(d['duplicate_accepts'] < 0 for d in details):
            raise AssertionError('acceptance counter is below unique stored points')
        if any(d['wrong_values'] for d in details):
            raise AssertionError(details)
        if event == 'store-handoff' and 'took over input from stopping instance' not in logs:
            raise AssertionError('no store input handoff')
        output = {'mode': mode, 'event': event, 'submitted': total,
                  'stores': details, 'source_reconnects': reconnects[0],
                  'before_failure_accepted': before_counts,
                  'fenced': event == 'store-handoff',
                  'client_wire_bytes': metric(client.prom, 'nanotube_tcp_socket_written_bytes_total')}
        print(json.dumps(output), flush=True)
        return output
    except BaseException:
        for proc in processes:
            for p in proc.root.glob('process-*.log'):
                print(str(p), p.read_text()[-3000:], flush=True)
        raise
    finally:
        for proc in reversed(processes):
            proc.close()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--scratch', type=Path, required=True)
    p.add_argument('--nanotube', type=Path, required=True)
    p.add_argument('--go-carbon', type=Path, required=True)
    p.add_argument('--points', type=int, default=300000)
    p.add_argument('--event', action='append', choices=['healthy', 'relay-usr2', 'relay-kill', 'client-restart', 'store-handoff'])
    p.add_argument('--mode', action='append', choices=['plaintext', 'stream'])
    a = p.parse_args()
    with tempfile.TemporaryDirectory(prefix='client-pool-', dir=a.scratch) as temp:
        for event in a.event or ['healthy', 'relay-usr2', 'relay-kill', 'client-restart', 'store-handoff']:
            for mode in a.mode or ['plaintext', 'stream']:
                run(Path(temp)/f'{event}-{mode}', a.nanotube.resolve(), a.go_carbon.resolve(), mode, event, a.points)


if __name__ == '__main__':
    main()
