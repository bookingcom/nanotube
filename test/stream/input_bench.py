#!/usr/bin/env python3
"""Local process-CPU comparison: plaintext/cache/stream input -> compressed stream output.

The receiver discards TCP bytes; output counters must prove every row drained.
All samples include process startup/shutdown and initial dictionaries. The large
repeated-name pass measures >99% input-ID hits. No storage CPU is included.
"""
import argparse, json, os, re, resource, signal, socket, subprocess, tempfile, threading, time, urllib.request
from pathlib import Path
from restart_e2e import RustProducer, wait_port
from e2e import port

class Discard:
    def __init__(self):
        self.listener=socket.socket();self.listener.bind(('127.0.0.1',0));self.port=self.listener.getsockname()[1]
        self.listener.listen();self.listener.settimeout(.2);self.stop=threading.Event();self.bytes=0;self.eof=threading.Event()
        self.thread=threading.Thread(target=self.run);self.thread.start()
    def run(self):
        while not self.stop.is_set():
            try:c,_=self.listener.accept()
            except socket.timeout:continue
            except OSError:return
            with c:
                c.settimeout(.2)
                while not self.stop.is_set():
                    try:b=c.recv(262144)
                    except socket.timeout:continue
                    if not b:self.eof.set();break
                    self.bytes+=len(b)
    def close(self):
        self.stop.set();self.listener.close();self.thread.join(5)

def samples(p):
    with urllib.request.urlopen(f'http://127.0.0.1:{p}/metrics',timeout=5) as r:lines=r.read().decode().splitlines()
    def value(name):
        return sum(float(l.rsplit(' ',1)[1]) for l in lines if l.startswith(name+' ') or l.startswith(name+'{'))
    return value

def run(root,binary,producer,names,points,mode,via_client=False):
    root.mkdir();out=Discard();incoming,prom=port(),port();nt=None;procs=[];client=None;client_log=None;client_prom=None
    clusters=root/'clusters.toml';rules=root/'rules.toml';config=root/'main.toml'
    clusters.write_text(f'''[[cluster]]
name = "destination"
type = "toall"
[[cluster.hosts]]
name = "127.0.0.1"
port = {out.port}
stream = true
[cluster.hosts.stream_options]
zstd = true
flush_period_ms = 25
[[cluster]]
name = "blackhole"
type = "blackhole"
''')
    rules.write_text('''[[rule]]
regexs = ["^example\\\\.drop\\\\."]
clusters = ["blackhole"]
[[rule]]
regexs = ["^example\\\\.special\\\\."]
clusters = ["destination"]
[[rule]]
prefixes = ["example."]
clusters = ["destination"]
''')
    config.write_text(f'''ClustersConfig = "{clusters}"
RulesConfig = "{rules}"
ListenTCP = "127.0.0.1:{incoming}"
PromPort = {prom}
PromLocal = true
PprofPort = -1
TermTimeoutSec = 30
MainQueueSize = {(points+1023)//1024+64}
MainQueueBatchSize = 1024
BatchFlushPerdiodSec = 1
WorkerPoolSize = 2
HostQueueSize = {points+4096}
TCPOutBufSize = 1048576
RouteCacheBytes = {64<<20 if mode=='plaintext-cache' else 0}
''')
    try:
        with (root/'relay.log').open('w') as log:
            nt=subprocess.Popen([str(binary),'-config',str(config)],stdout=log,stderr=log)
            wait_port(prom);wait_port(incoming)
            source_port = incoming
            if via_client:
                source_port, client_prom = port(), port()
                (root/'client-clusters').write_text(f'''[[cluster]]
name="relays"
type="sticky"
stream={str(mode == 'stream').lower()}
[cluster.stream_options]
zstd=true
flush_period_ms=25
[[cluster.hosts]]
name="127.0.0.1"
port={incoming}
''')
                (root/'client-rules').write_text('[[rule]]\nprefixes=["example."]\nclusters=["relays"]\n')
                (root/'client-main').write_text(f'''ClustersConfig="{root/'client-clusters'}"
RulesConfig="{root/'client-rules'}"
ListenTCP="127.0.0.1:{source_port}"
PromPort={client_prom}
PromLocal=true
PprofPort=-1
TermTimeoutSec=30
MainQueueSize={(points+1023)//1024+64}
MainQueueBatchSize=1024
HostQueueSize={points+4096}
WorkerPoolSize=2
TCPOutBufSize=4096
TCPOutBufFlushPeriodSec=1
TCPOutConnectionRefreshPeriodSec=0
''')
                client_log=(root/'client.log').open('w')
                client=subprocess.Popen([str(binary),'-config',str(root/'client-main')],stdout=client_log,stderr=client_log)
                procs.append(client)
                wait_port(client_prom);wait_port(source_port)
            args=[str(producer),f'127.0.0.1:{source_port}',str(names),'1700000000']
            if mode!='stream' or via_client:args.append('plaintext')
            p=subprocess.Popen(args,stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True);procs.append(p)
            start=time.monotonic();p.stdin.write(f'send 0 {points}\nquit\n');p.stdin.flush();p.stdin.close()
            answer=p.stdout.readline().strip();p.stdout.close()
            if p.wait(timeout=90)!=0 or not answer.startswith(f'sent {points} bytes '):raise AssertionError(answer)
            deadline=time.monotonic()+60
            while True:
                metrics=samples(prom)
                if metrics('nanotube_tcp_flushed_records_total')==points:break
                if time.monotonic()>deadline:raise AssertionError('output did not drain')
                time.sleep(.02)
            wall=time.monotonic()-start
            if metrics('nanotube_in_records_total')!=points or metrics('nanotube_throttled_records_total') or metrics('nanotube_throttled_host_records_total'):raise AssertionError('input/drop accounting differs')
            hits=metrics('nanotube_stream_input_route_hits_total') if mode=='stream' else metrics('nanotube_route_cache_hits_total')
            nt.send_signal(signal.SIGTERM)
            _,status,usage=os.wait4(nt.pid,0);nt.returncode=os.waitstatus_to_exitcode(status)
            if nt.returncode!=0:raise AssertionError(f'relay exit {nt.returncode}')
            if not out.eof.wait(5):raise AssertionError('output socket did not drain to EOF')
            client_wire = None
            client_cpu = None
            if client is not None:
                cm = samples(client_prom)
                if cm('nanotube_tcp_flushed_records_total') != points or cm('nanotube_throttled_host_records_total'):
                    raise AssertionError('client did not drain exactly')
                client_wire = cm('nanotube_tcp_socket_written_bytes_total')
                client.send_signal(signal.SIGTERM)
                _, status, cu = os.wait4(client.pid, 0)
                client.returncode = os.waitstatus_to_exitcode(status)
                if client.returncode:raise AssertionError('client failed during drain')
                client_cpu = (cu.ru_utime+cu.ru_stime)*1e9/points
            result=dict(via_client=via_client,client_cpu_ns_per_point=client_cpu,route_bytes=metrics("nanotube_stream_input_route_bytes"), dictionary_bytes=metrics("nanotube_stream_input_dictionary_bytes"), input_wire_B_per_point=(client_wire if via_client else int(answer.split()[3])+(10 if mode=='stream' else 0))/points,mode=mode,points=points,peak_rss_bytes=usage.ru_maxrss if __import__("sys").platform == "darwin" else usage.ru_maxrss*1024,cpu_s=usage.ru_utime+usage.ru_stime,cpu_ns_per_point=(usage.ru_utime+usage.ru_stime)*1e9/points,wall_s=wall,route_hit_percent=100*hits/points,output_wire_B_per_point=out.bytes/points)
            print(json.dumps(result),flush=True);return result
    except BaseException:
        print((root/'relay.log').read_text()[-4000:],flush=True);raise
    finally:
        for p in procs:
            if p.poll() is None:p.kill();p.wait()
        if nt and nt.returncode is None:nt.kill();nt.wait()
        if client_log is not None:client_log.close()
        out.close()

def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--scratch',type=Path,required=True);p.add_argument('--rust-producer',type=Path,required=True);p.add_argument('--points',type=int,default=2000000);p.add_argument('--rounds',type=int,default=3);p.add_argument('--names-file',type=Path);p.add_argument('--nanotube',type=Path);p.add_argument('--modes',nargs='+',choices=['plaintext','plaintext-cache','stream']);p.add_argument('--via-client',action='store_true',help='send plaintext through a real sticky client; measure the receiving relay');a=p.parse_args()
    repo=Path(__file__).resolve().parents[2]
    with tempfile.TemporaryDirectory(prefix='input-bench-',dir=a.scratch) as d:
        root=Path(d);binary=root/'nanotube';env=dict(os.environ,TMPDIR=d)
        if a.nanotube:binary=a.nanotube.resolve()
        else:subprocess.run(['go','build','-o',str(binary),'./cmd/nanotube'],cwd=repo,env=env,check=True)
        names=root/'names.txt';names.write_text(''.join('example.'+'application.component.worker.region.zone.'*5+f'metric_{i:06d}\n' for i in range(16384)))
        if a.names_file:names=a.names_file.resolve()
        for n in range(a.rounds):
            modes=list(a.modes or ['plaintext','plaintext-cache','stream'])
            if n%2:modes.reverse()
            for mode in modes:run(root/f'{n}-{mode}',binary,a.rust_producer.resolve(),names,a.points,mode,a.via_client)
if __name__=='__main__':main()
