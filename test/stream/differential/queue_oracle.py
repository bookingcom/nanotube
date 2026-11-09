#!/usr/bin/env python3
"""Compile the paused-consumer queue oracle at two immutable/runtime trees.

Only a test file is overlaid; runtime queue/routing code comes from each tree.
Compare complete accepted point ledgers, replica destinations and accounting.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--baseline', type=Path, required=True)
parser.add_argument('--candidate', type=Path, required=True)
parser.add_argument('--scratch', type=Path, required=True)
args = parser.parse_args()
template = Path(__file__).with_name('queue_oracle.go.txt').read_text()
legacy = '''for _,cl:=range clusters { if err:=cl.PushBytesBatch(r,ms,batch);err!=nil {t.Fatal(err)} }'''
plain_snapshot = '''for _,r:=range c.recs[:c.n] {v,ts,_:=r.TypedValues();out.Rows=append(out.Rows,row{id,string(r.Path),math.Float64bits(v),ts})}'''
env = os.environ.copy()
env.update(GOTOOLCHAIN='go1.27.0', GOFLAGS='-mod=vendor',
           TMPDIR=str(args.scratch/'go-tmp'), GOTMPDIR=str(args.scratch/'go-tmp'),
           GOCACHE=str(args.scratch/'go-cache'))
with tempfile.TemporaryDirectory(prefix='queue-oracle-', dir=args.scratch) as temp:
    root = Path(temp)
    ledgers = []
    for label, tree in [('baseline', args.baseline), ('candidate', args.candidate)]:
        source = template
        setup, publish, snapshot = '', legacy, plain_snapshot
        if label == 'candidate':
            setup = 'var plans TypedRoutes;var tokens [1000]uint64;var route []ClusterTarget;for _,cl:=range clusters {route=append(route,cl)}'
            publish = '''if mixed && i%3==1 {'''+legacy+'''} else { p:=plans.Resolve(route,r.PathHash());if p==nil {t.Fatal("missing route")};p.Push(name,r.PathHash(),value,stamp,&tokens[i],batch) }'''
            snapshot = '''if c.points!=nil {for _,p:=range c.points[:c.n] {out.Rows=append(out.Rows,row{id,string(p.name),math.Float64bits(p.value),p.timestamp})}} else {'''+plain_snapshot+'''}'''
        source = source.replace('// ORACLE_SETUP', setup).replace('// ORACLE_PUBLISH', publish).replace('// ORACLE_SNAPSHOT', snapshot)
        path = root/(label+'.go');path.write_text(source)
        overlay = root/(label+'.json');overlay.write_text(json.dumps({'Replace':{str(tree/'pkg/target/queue_oracle_test.go'):str(path)}}))
        binary = root/(label+'.test')
        subprocess.run(['go','test','-vet=off','-c','-p','1','-overlay='+str(overlay),'-o',str(binary),'./pkg/target'],cwd=tree,env=env,check=True,timeout=170)
        completed = subprocess.run([str(binary),'-test.run=^TestQueueDifferentialOracle$','-test.timeout=170s'],env=env,check=True,capture_output=True,timeout=175)
        line = next(line for line in completed.stdout.splitlines() if line.startswith(b'QUEUE_ORACLE '))
        ledgers.append(json.loads(line[len(b'QUEUE_ORACLE '):]))
    if ledgers[0] != ledgers[1]:
        raise AssertionError('baseline/candidate queue ledger differs')
    canonical = json.dumps(ledgers[0],sort_keys=True,separators=(',',':')).encode()
    print(json.dumps(dict(result='PASS',cases=len(ledgers[0]),sha256=hashlib.sha256(canonical).hexdigest(),
                         accepted_points=sum(len(x['Rows']) for x in ledgers[0]),
                         comparison='full per-host ordered name bytes, float bits, timestamps, drop counts, pending point/byte counts')))
