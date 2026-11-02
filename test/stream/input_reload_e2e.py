#!/usr/bin/env python3
"""Opt-in local reload oracle: persistent inputs -> relay -> two real stores.

Reuse restart_e2e's receiver config/counters and client_pool_e2e's independent
field oracle. Supply prebuilt binaries and a scratch directory. All generated
state is removed, and process groups are killed/waited, even on failures.
"""
import argparse
import ctypes
import json
import os
from pathlib import Path
import signal
import struct
import subprocess
import sys
import tempfile
import threading
import time

from e2e import port
from restart_e2e import carbon_config, counter, wait_port
from client_pool_e2e import Process, observe_store


def usage(pid):
    if sys.platform == 'darwin':
        data = ctypes.create_string_buffer(256)
        if ctypes.CDLL('/usr/lib/libproc.dylib').proc_pid_rusage(pid, 2, ctypes.byref(data)):
            return None
        user, system = struct.unpack_from('QQ', data.raw, 16)
        timebase = (ctypes.c_uint32*2)()
        ctypes.CDLL(None).mach_timebase_info(ctypes.byref(timebase))
        return (user+system)*timebase[0]/timebase[1]/1e9, struct.unpack_from('Q', data.raw, 64)[0]
    try:
        fields = Path(f'/proc/{pid}/stat').read_text().split()
        return (int(fields[13])+int(fields[14]))/os.sysconf('SC_CLK_TCK'), int(fields[23])*os.sysconf('SC_PAGE_SIZE')
    except (OSError, ValueError):
        return None


def entries(path):
    result = []
    for line in path.read_text().splitlines():
        try:
            result.append(json.loads(line))
        except ValueError:
            pass
    return result


def run(root, args):
    stores, children, group_ids = [], [], []
    logs = []
    stop_sample = threading.Event()
    sampler = None
    root.mkdir()
    try:
        for index in range(2):
            d = root/f'store{index}'
            d.mkdir()
            (d/'data').mkdir()
            (d/'dump').mkdir()
            (d/'schemas.conf').write_text('[all]\npattern=.*\nretentions=1s:24h\n')
            incoming, http, prom = port(), port(), port()
            cfg = d/'config'
            cfg.write_text(carbon_config(d, incoming, http, prom))
            store = Process(d, args.carbon, cfg, incoming, prom)
            store.http = http
            stores.append(store)
        incoming, prom = port(), port()
        clusters = ['[[cluster]]', 'name="stores"', 'type="toall"',
                    'stream=true', '[cluster.stream_options]',
                    'zstd=true', 'flush_period_ms=25']
        for store in stores:
            clusters += ['[[cluster.hosts]]', 'name="127.0.0.1"', f'port={store.incoming}']
        (root/'clusters').write_text('\n'.join(clusters)+'\n')
        (root/'rules').write_text('[[rule]]\nprefixes=["example."]\nclusters=["stores"]\n')
        cfg = root/'config'
        cfg.write_text(f'''ClustersConfig="{root/'clusters'}"
RulesConfig="{root/'rules'}"
ListenTCP="127.0.0.1:{incoming}"
PromPort={prom}
PromLocal=true
PprofPort=-1
TermTimeoutSec=30
IncomingConnIdleTimeoutSec=3600
MainQueueSize=8192
MainQueueBatchSize=256
BatchFlushPerdiodSec=1
TCPMainQueueBytes=67108864
HostQueueSize={args.points+65536}
WorkerPoolSize=2
TCPOutBufSize=65536
TCPOutBufFlushPeriodSec=1
TCPOutConnectionRefreshPeriodSec=0
SendTimeoutSec=5
RouteCacheBytes=0
LogLimitInitial=1000000
StreamInputMaxDictionaryBytes=268435456
StreamInputMaxRouteBytes=8388608
StreamInputTotalBytes=17179869184
''')
        active = root/'active-nanotube'
        active.symlink_to(args.nanotube)
        log_path = root/'relay.log'
        log = log_path.open('w')
        logs.append(log)
        relay = subprocess.Popen([str(active), '-config', str(cfg)], cwd=root,
                                 stdout=log, stderr=log, start_new_session=True)
        children.append(relay)
        group_ids.append(relay.pid)
        wait_port(incoming)
        wait_port(prom)
        cpu, samples = {}, []
        lock = threading.Lock()

        def sample():
            while not stop_sample.wait(.02):
                pids = {relay.pid} | {int(e['pid']) for e in entries(log_path) if 'pid' in e}
                rss = 0
                for pid in pids:
                    u = usage(pid)
                    if u:
                        cpu[pid] = max(cpu.get(pid, 0), u[0])
                        rss += u[1]
                with lock:
                    samples.append((time.time(), sum(cpu.values()), rss))
        sampler = threading.Thread(target=sample)
        sampler.start()
        base = int(time.time())-(args.points+args.names-1)//args.names-120
        producer_log = (root/'producer.log').open('w')
        logs.append(producer_log)
        producer = subprocess.Popen([str(args.producer), '-address', f'127.0.0.1:{incoming}',
            '-mode', args.mode, '-points', str(args.points), '-names', str(args.names),
            '-base', str(base), '-lanes', str(args.lanes), '-chunk', str(args.chunk),
            '-delay', args.delay], stdout=subprocess.PIPE, stderr=producer_log, text=True)
        children.append(producer)
        if producer.stdout.readline().strip() != 'ready':
            raise AssertionError('producer failed to prime inputs')
        windows = []
        pid = relay.pid
        for number in range(args.reloads):
            time.sleep(args.interval)
            if args.successor:
                replacement = root/'next-link'
                replacement.symlink_to(args.successor)
                replacement.replace(active)
            started = time.time()
            os.kill(pid, signal.SIGUSR2)
            deadline = time.monotonic()+60
            while time.monotonic() < deadline:
                records = entries(log_path)
                successors = [int(e['pid']) for e in records
                              if e.get('msg', '').endswith('Started new process. Moved FDs.')]
                terminated = sum(e.get('msg') == 'Terminated gracefully.' for e in records)
                if len(successors) >= number+1 and terminated >= number+1:
                    pid = successors[-1]
                    windows.append((started, time.time()))
                    break
                if relay.poll() not in (None, 0):
                    raise AssertionError('relay failed during reload')
                time.sleep(.01)
            else:
                raise AssertionError('reload did not complete')
        output = producer.stdout.read()
        if producer.wait(timeout=30):
            raise AssertionError('producer failed: '+(root/'producer.log').read_text())
        source = json.loads(output)
        last, stable = None, 0
        for _ in range(150):
            counts = [counter(s.prom) for s in stores]
            stable = stable+1 if counts == last else 0
            last = counts
            if counts == [args.points]*2 or stable >= 20:
                break
            time.sleep(.1)
        names = ['example.'+'application.component.worker.region.zone.'*5+f'metric_{i:06d}'
                 for i in range(args.names)]
        details = []
        for store in stores:
            for attempt in range(15):
                result = observe_store(store.http, names, args.points, base)
                if result['missing'] == 0 or attempt >= 5:
                    break
                time.sleep(1)
            result['accepted'] = counter(store.prom)
            result['duplicate_accepts'] = result['accepted']-result['unique']
            details.append(result)
        records = entries(log_path)
        pauses, gaps, transfers = {}, [], []
        for record in records:
            peer = record.get('peer')
            if record.get('msg') == 'Reload: paused input reader':
                pauses[peer] = record['ts']
            if record.get('msg') == 'Reload: input read-ahead started' and peer in pauses:
                gaps.append(record['ts']-pauses.pop(peer))
            if record.get('msg') == 'Reload: handed off accepted input':
                transfers.append(record['handoff_seconds'])
        reload_usage = []
        with lock:
            for start, end in windows:
                before = [s for s in samples if s[0] <= start]
                during = [s for s in samples if start <= s[0] <= end]
                after = [s for s in samples if s[0] >= end]
                if before and during:
                    post = after[0] if after else during[-1]
                    reload_usage.append(dict(wall_seconds=end-start, cpu_seconds=post[1]-before[-1][1],
                                             peak_overlap_rss_bytes=max(s[2] for s in during)))
        errors = [r for r in records if r.get('level') == 'error']
        result = dict(mode=args.mode, lanes=args.lanes, points=args.points, reloads=args.reloads,
                      initial=str(args.nanotube), successor=str(args.successor or args.nanotube),
                      stores=details, source=source, pause_to_read_ahead_seconds=gaps,
                      transfer_seconds=transfers, reload_usage=reload_usage,
                      relay_cpu_seconds=sum(cpu.values()),
                      peak_overlap_rss_bytes=max((s[2] for s in samples), default=0),
                      relay_errors=errors)
        print(json.dumps(result), flush=True)
        if args.expect_zero and (any(d['missing'] or d['duplicate_accepts'] or d['wrong_values'] for d in details)
                                 or any(c['errors'] for c in source['connections']) or errors):
            raise AssertionError('exact reload gate failed')
        return result
    except BaseException:
        if 'log_path' in locals():
            print(log_path.read_text()[-12000:], file=sys.stderr)
        raise
    finally:
        stop_sample.set()
        if sampler:
            sampler.join(5)
        for group in group_ids:
            try:
                os.killpg(group, signal.SIGKILL)
            except ProcessLookupError:
                pass
        for child in children:
            if child.poll() is None:
                child.kill()
            child.wait(timeout=10)
        for store in stores:
            store.close()
        for log in logs:
            log.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['scratch', 'nanotube', 'carbon', 'producer']:
        parser.add_argument('--'+name, type=Path, required=True)
    parser.add_argument('--successor', type=Path)
    parser.add_argument('--mode', choices=['plaintext', 'zstd', 'zstd-flush', 'stream', 'mixed'], required=True)
    parser.add_argument('--points', type=int, default=300000)
    parser.add_argument('--names', type=int, default=128)
    parser.add_argument('--lanes', type=int, default=1)
    parser.add_argument('--chunk', type=int, default=512)
    parser.add_argument('--delay', default='2ms')
    parser.add_argument('--reloads', type=int, default=3)
    parser.add_argument('--interval', type=float, default=.2)
    parser.add_argument('--expect-zero', action='store_true')
    args = parser.parse_args()
    for name in ['scratch', 'nanotube', 'carbon', 'producer', 'successor']:
        value = getattr(args, name)
        if value:
            setattr(args, name, value.resolve())
    with tempfile.TemporaryDirectory(prefix='reload-', dir=args.scratch) as tmp:
        run(Path(tmp)/'case', args)


if __name__ == '__main__':
    main()
