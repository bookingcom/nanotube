#!/usr/bin/env python3
"""Pair unchanged legacy routing with the fetched base in short alternating windows.

Run from the repository root with the repository Go toolchain selected. The
script creates one temporary package test, compiles it, removes it, then records
2048 pairs of 4096 records for each of scalar/batch and 3/40-host layouts. Output
contains per-process raw timings; compare run medians as well as individual
windows because shared-machine timing samples are correlated.
"""
import argparse
import json
import os
import statistics
import tempfile
import subprocess,pathlib,re
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--base',default='6adeac277595c3c238597dc05b14af33aa60ac8a')
parser.add_argument('--output',type=pathlib.Path,required=True)
parser.add_argument('--runs',type=int,default=5)
args=parser.parse_args()
if args.runs<1:parser.error('--runs must be positive')
s=subprocess.check_output(['git','show',args.base+':pkg/target/cluster.go'],text=True)
def block(start):
 i=s.index(start);a=s.index('{',i);depth=1;b=a+1
 while depth:
  if s[b]=='{':depth+=1
  elif s[b]=='}':depth-=1
  b+=1
 return s[i:b]
imports=s[s.index('import ('):s.index('\n)',s.index('import ('))+2]
parts=[block('type Cluster struct')]+[block('func (cl *Cluster) '+n+'(') for n in ['availHostsList','PushBytes','PushBytesBatch','GetName','resolveHosts']]
text='package target\n'+imports.replace('import (','import (\n "testing"\n "time"\n "encoding/json"\n "os"')+'\n'+'\n'.join(parts)
text=re.sub(r'\bCluster\b','legacyComparisonCluster',text)
text+='''
func TestPairedLegacyWithinProcess(t *testing.T){
 type sample struct{Hosts int;Batch bool;Round int;Base,Current int64}
 var samples []sample
 for _,size:=range []int{3,40}{for _,batching:=range []bool{false,true}{
  old:=&legacyComparisonCluster{Type:conf.JumpCluster};cur:=&Cluster{Type:conf.JumpCluster}
  for i:=0;i<size;i++{target:=&campaignBenchTarget{};old.Hosts=append(old.Hosts,target);cur.Hosts=append(cur.Hosts,target)}
  records:=make([]*rec.RecBytes,4096);for i:=range records{records[i]=&rec.RecBytes{Path:[]byte(fmt.Sprintf("example.application.worker.%d.requests",i))};records[i].PathHash()}
  oldBatch,curBatch:=NewBatch(),NewBatch()
  base:=func()int64{start:=time.Now();for _,p:=range records{if batching{_ = old.PushBytesBatch(p,nil,oldBatch)}else{_ = old.PushBytes(p,nil)}};return time.Since(start).Nanoseconds()}
  current:=func()int64{start:=time.Now();for _,p:=range records{if batching{_ = cur.PushBytesBatch(p,nil,curBatch)}else{_ = cur.PushBytes(p,nil)}};return time.Since(start).Nanoseconds()}
  for i:=0;i<64;i++{base();current()}
  for i:=0;i<2048;i++{var a,b int64;if i%2==0{a=base();b=current()}else{b=current();a=base()};samples=append(samples,sample{size,batching,i,a,b})}
  oldBatch.Flush();curBatch.Flush()
 }}
 b,e:=json.Marshal(samples);if e!=nil{t.Fatal(e)};if e=os.WriteFile(os.Getenv("PAIRED_RESULT"),b,0600);e!=nil{t.Fatal(e)}
}
'''
harness=pathlib.Path('pkg/target/legacy_comparison_test.go')
if harness.exists():raise SystemExit('refusing to replace '+str(harness))
args.output.mkdir(parents=True,exist_ok=True)
with tempfile.TemporaryDirectory(prefix='placement-legacy-') as temp:
 binary=pathlib.Path(temp)/'compare.test'
 try:
  harness.write_text(text)
  subprocess.run(['gofmt','-w',str(harness)],check=True)
  subprocess.run(['go','test','-c','-o',str(binary),'./pkg/target'],check=True)
 finally:
  harness.unlink(missing_ok=True)
 summaries={}
 for run in range(args.runs):
  output=(args.output/f'paired-{run}.json').resolve()
  subprocess.run([str(binary),'-test.run=^TestPairedLegacyWithinProcess$','-test.count=1'],env=dict(os.environ,GOMAXPROCS='1',PAIRED_RESULT=str(output)),check=True)
  groups={}
  for row in json.loads(output.read_text()):groups.setdefault(f"hosts={row['Hosts']}/batch={row['Batch']}",[]).append(row['Current']/row['Base']-1)
  for group,ratios in groups.items():summaries.setdefault(group,[]).append(100*statistics.median(ratios))
 print(json.dumps({'paired_median_delta_percent_by_process':summaries},indent=2))
