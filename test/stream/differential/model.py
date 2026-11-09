#!/usr/bin/env python3
"""Reproduce matched Linux process-rusage relay models without editing either tree.

Go test overlays supply the same producer/receiver fixture harness to baseline
and candidate. Runtime code remains the code in each tree. Docker networking is
isolated; TCP stays within each container. All transient outputs are removed.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

parser = argparse.ArgumentParser(description=__doc__)
for name in ['baseline', 'candidate', 'scratch', 'results']:
    parser.add_argument('--'+name, type=Path, required=True)
parser.add_argument('--runs', type=int, default=5)
parser.add_argument('--cycles', type=int, default=256)
parser.add_argument('--names', type=int, default=1081344)
parser.add_argument('--arch', choices=['arm64', 'amd64'], default='arm64')
parser.add_argument('--image', default='golang:1.27-bookworm')
parser.add_argument('--cold-only', action='store_true', help='measure fresh dictionary initialization only')
args = parser.parse_args()
for name in ['baseline', 'candidate', 'scratch', 'results']:
    setattr(args, name, getattr(args, name).resolve())
args.results.mkdir(parents=True, exist_ok=True)
if args.names <= 0 or args.names % 33 or args.cycles <= 0 or args.runs <= 0:
    parser.error('names must be a positive multiple of 33; cycles/runs must be positive')
files = ['cmd/nanotube/'+name for name in [
    'stream_input.go', 'stream_input_names.go', 'route_cache.go',
    'stream_input_bench_test.go', 'stream_relay_bench_test.go',
    'stream_relay_fixture_test.go']]


def disk_gate():
    if shutil.disk_usage(args.scratch).free < 25*1024**3:
        raise RuntimeError('free disk below 25 GiB')


with tempfile.TemporaryDirectory(prefix='model-', dir=args.scratch) as temporary:
    root = Path(temporary)
    for name in ['tmp', 'cache']:
        (root/name).mkdir()
    env = os.environ.copy()
    env.update(GOTOOLCHAIN='go1.27.0', GOFLAGS='-mod=vendor', GOOS='linux',
               GOARCH=args.arch, CGO_ENABLED='0', TMPDIR=str(root/'tmp'),
               GOTMPDIR=str(root/'tmp'), GOCACHE=str(root/'cache'))
    binaries = {}
    for label, tree in [('baseline', args.baseline), ('candidate', args.candidate)]:
        disk_gate()
        overlay = root/(label+'.json')
        overlay.write_text(json.dumps({'Replace':{
            str(tree/path): str(args.candidate/path) for path in files[-2:]
            if tree != args.candidate}}))
        binary = root/(label+'.test')
        subprocess.run(['go','test','-vet=off','-c','-p','1',
                        '-overlay='+str(overlay),'-o',str(binary),*files],
                       cwd=tree,env=env,check=True,timeout=170)
        binaries[label] = binary
    fixture = root/'fixture.bin'
    base = ['docker','run','--rm','--network','none','--cpus','8','--memory','6g',
            '-v',str(root)+':'+str(root),'-e','GOMAXPROCS=8','-e','GOGC=100',
            '-e','NANOTUBE_RELAY_MODEL=1','-e','RELAY_MODEL_NAMES='+str(args.names),
            '-e','RELAY_MODEL_CYCLES='+str(args.cycles)]
    if args.cold_only:
        base += ['-e','RELAY_MODEL_COLD_ONLY=1']
    disk_gate()
    subprocess.run(base+['-e','RELAY_MODEL_FIXTURE_OUT='+str(fixture),
                        args.image,str(binaries['baseline']),
                        '-test.run=^TestStreamRelayModel$','-test.timeout=170s'],
                   check=True,timeout=175)
    with fixture.open('rb') as source:
        digest = hashlib.file_digest(source, 'sha256').hexdigest()
    rows = []
    for trial in range(1,args.runs+1):
        order = ['baseline','candidate'] if trial % 2 else ['candidate','baseline']
        for label in order:
            disk_gate()
            command = base+['-e','RELAY_MODEL_FIXTURE='+str(fixture),args.image,
                            str(binaries[label]),'-test.run=^TestStreamRelayModel$',
                            '-test.timeout=170s']
            log = args.results/f'model-{label}-{trial}.log'
            with log.open('w') as output:
                completed = subprocess.run(command,stdout=output,
                                           stderr=subprocess.STDOUT,timeout=175)
            rows.append(dict(trial=trial,revision=label,command=command,
                             exit=completed.returncode,log=log.name))
            (args.results/'model-runs.json').write_text(json.dumps(dict(
                fixture_sha256=digest,names=args.names,cycles=args.cycles,cold_only=args.cold_only,
                baseline=str(args.baseline),candidate=str(args.candidate),
                rows=rows),indent=2)+'\n')
            print(trial,label,completed.returncode,flush=True)
            completed.check_returncode()
