"""Main-only release with one previous ELF and graceful shutdown."""
import datetime, hashlib, json, os, pathlib, shlex, shutil, signal, subprocess, sys, time, urllib.request
ROOT=pathlib.Path('/home/bsc/fullnode')
SRC=pathlib.Path('/home/bsc/codex_stage/arb-node-build/build/bin/geth')
EVID=pathlib.Path(sys.argv[1])
EVID.mkdir(parents=True,exist_ok=False)
EXPECTED=sys.argv[2]
allow={'ARB_V3_CACHE_MODE','ARB_SINGLETON_EXT_TICKS_PER_SIDE','ARB_SINGLETON_WINDOW_RADIUS','ARB_V3_TICKS_PER_SIDE','ARB_V3_WINDOW_RADIUS','ARB_SYNC_HEADER_TIMEOUT_MS','ARB_TX_ARRIVE_TIMEOUT_MS','GOGC','GOMEMLIMIT','GOMAXPROCS','GODEBUG','GOTRACEBACK'}
def utc(): return datetime.datetime.now(datetime.timezone.utc).isoformat()
def sha(p):
 h=hashlib.sha256()
 with open(p,'rb') as f:
  for b in iter(lambda:f.read(1048576),b''):h.update(b)
 return h.hexdigest()
def ticks(pid): return pathlib.Path('/proc',str(pid),'stat').read_text().rsplit(') ',1)[1].split()[19]
def live(pid):
 try:
  return pathlib.Path('/proc',str(pid),'stat').read_text().rsplit(') ',1)[1].split()[0]!='Z'
 except FileNotFoundError:return False
def save(name,data):
 p=EVID/name;p.write_text(json.dumps(data,indent=2)+'\n');os.chmod(p,0o600)
def rpc(method,params=[]):
 req=urllib.request.Request('http://127.0.0.1:8547',data=json.dumps({'jsonrpc':'2.0','id':1,'method':method,'params':params}).encode(),headers={'Content-Type':'application/json'})
 obj=json.load(urllib.request.urlopen(req,timeout=3))
 if 'error' in obj: raise RuntimeError('rpc '+method+':'+str(obj['error']))
 return obj['result']
pids=subprocess.check_output(['pgrep','-x','geth'],text=True).split()
assert len(pids)==1
OLD_PID=int(pids[0]);OLD_START=ticks(OLD_PID)
OLD_SHA=sha('/proc/'+str(OLD_PID)+'/exe');NEW_SHA=sha(SRC)
CONFIG_SHA=sha(ROOT/'config.toml');SCRIPT_SHA=sha(ROOT/'z_run.sh')
metadata=subprocess.check_output(['/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64/bin/go','version','-m',str(SRC)],text=True)
assert 'vcs.revision='+EXPECTED in metadata and 'vcs.modified=false' in metadata
assert ticks(OLD_PID)==OLD_START
assert os.readlink('/proc/'+str(OLD_PID)+'/exe')==str(ROOT/'geth')
assert sha('/proc/'+str(OLD_PID)+'/exe')==OLD_SHA==sha(ROOT/'geth')
assert not (ROOT/'geth').is_symlink()
assert sha(SRC)==NEW_SHA
assert sha(ROOT/'config.toml')==CONFIG_SHA and sha(ROOT/'z_run.sh')==SCRIPT_SHA
assert subprocess.check_output(['git','-C',str(SRC.parents[2]),'branch','--show-current'],text=True).strip()=='main'
assert subprocess.check_output(['git','-C',str(SRC.parents[2]),'rev-parse','HEAD'],text=True).strip()==EXPECTED
assert subprocess.check_output(['git','-C',str(SRC.parents[2]),'status','--porcelain'],text=True).strip()==''
argv=pathlib.Path('/proc',str(OLD_PID),'cmdline').read_bytes().rstrip(b'\0').decode().split('\0')
script=(ROOT/'z_run.sh').read_text()
execs=[line.strip()[5:] for line in script.splitlines() if line.strip().startswith('exec ')]
assert len(execs)==1 and shlex.split(execs[0])==argv
runtime={}
for item in pathlib.Path('/proc',str(OLD_PID),'environ').read_bytes().split(b'\0'):
 k,sep,v=item.partition(b'=')
 name=k.decode(errors='replace')
 if sep and name in allow:runtime[name]=v.decode()

env={k.decode():v.decode() for item in pathlib.Path('/proc',str(OLD_PID),'environ').read_bytes().split(b'\0') if b'=' in item for k,v in [item.split(b'=',1)]}
pre={'utc':utc(),'old_pid':OLD_PID,'old_start_ticks':OLD_START,'old_sha256':OLD_SHA,'new_sha256':NEW_SHA,
     'new_commit':EXPECTED,'argv_matches_z_run':True,'argv':argv,'runtime_tuning':runtime,
     'config_sha256':CONFIG_SHA,'z_run_sha256':SCRIPT_SHA,'before_rpc':{'chain_id':rpc('eth_chainId'),'syncing':rpc('eth_syncing'),'head':rpc('eth_blockNumber'),'peers':rpc('net_peerCount')}}
assert pre['before_rpc']['chain_id']=='0x38' and pre['before_rpc']['syncing'] is False
save('pre-restart.json',pre)
candidate=ROOT/'.geth.next'
backup=ROOT/'.geth.prev.next'
assert not candidate.exists() and not backup.exists()
shutil.copy2(SRC,candidate);os.chmod(candidate,0o755)
shutil.copy2(ROOT/'geth',backup)
for p in (candidate,backup):
 with p.open('rb') as f:os.fsync(f.fileno())
assert sha(candidate)==NEW_SHA and sha(backup)==OLD_SHA
os.replace(backup,ROOT/'geth.prev')
assert sha(ROOT/'geth.prev')==OLD_SHA
print('ROLLBACK_PREPARED',OLD_SHA,flush=True)
result={'commit':EXPECTED,'new_sha256':NEW_SHA,'old_sha256':OLD_SHA,'old_pid':OLD_PID,'rollback_used':False,'pre_restart':pre}
new_proc=None
stopped=False
def launch():
 with (ROOT/'geth-stdout.log').open('ab',buffering=0) as log:
  return subprocess.Popen(['/bin/bash',str(ROOT/'z_run.sh')],cwd=str(ROOT),env=env,stdin=subprocess.DEVNULL,stdout=log,stderr=subprocess.STDOUT,start_new_session=True,close_fds=True)
def wait_exit(pid,limit=180):
 start=time.monotonic();last=start
 while live(pid):
  if time.monotonic()-start>limit:raise RuntimeError('graceful shutdown timeout, no forced kill issued')
  if time.monotonic()-last>10:
   print('WAITING_GRACEFUL_SHUTDOWN',pid,round(time.monotonic()-start,1),flush=True);last=time.monotonic()
  time.sleep(.5)
 return time.monotonic()-start
try:
 result['signal_utc']=utc()
 os.kill(OLD_PID,signal.SIGINT)
 print('SIGINT_SENT',OLD_PID,flush=True)
 result['shutdown_seconds']=wait_exit(OLD_PID)
 stopped=True;result['stopped_utc']=utc()
 print('OLD_NODE_EXITED',round(result['shutdown_seconds'],2),flush=True)
 assert sha(ROOT/'geth')==OLD_SHA and sha(ROOT/'config.toml')==CONFIG_SHA and sha(ROOT/'z_run.sh')==SCRIPT_SHA
 os.replace(candidate,ROOT/'geth')
 new_proc=launch();result['new_pid']=new_proc.pid;result['launched_utc']=utc()
 print('NEW_NODE_LAUNCHED',new_proc.pid,flush=True)
 start=time.monotonic();last=start;previous=None
 while time.monotonic()-start<180:
  if new_proc.poll() is not None:raise RuntimeError('new node exited with code '+str(new_proc.returncode))
  try:
   version=rpc('web3_clientVersion');chain=rpc('eth_chainId');head=int(rpc('eth_blockNumber'),16)
   if EXPECTED[:8] not in version or chain!='0x38':raise RuntimeError('new node identity/chain mismatch')
   page=rpc('debug_pendingTiming',['0x0','0x1'])
   if page['sample_every']!=32 or page['capacity']!=65536:raise RuntimeError('timing capability mismatch')
   if previous is not None and head>previous and rpc('eth_syncing') is False:
    result['ready_utc']=utc();result['startup_seconds']=time.monotonic()-start
    result['ready_rpc']={'version':version,'chain_id':chain,'head':head,'peers':int(rpc('net_peerCount'),16),'syncing':False,
                         'timing':{k:page[k] for k in ['boot_id','sample_every','capacity','oldest_sequence','latest_sequence','next_sequence','overwritten','lost_since_cursor']}}
    break
   previous=head
  except Exception as e:
   result['last_readiness_error']=str(e)[:400]
  if time.monotonic()-last>10:
   print('WAITING_NODE_READINESS',round(time.monotonic()-start,1),flush=True);last=time.monotonic()
  time.sleep(1)
 else:raise RuntimeError('new node readiness timeout')
 assert sha('/proc/'+str(new_proc.pid)+'/exe')==NEW_SHA==sha(ROOT/'geth')
 assert pathlib.Path('/proc',str(new_proc.pid),'comm').read_text().strip()=='geth'
 assert os.readlink('/proc/'+str(new_proc.pid)+'/cwd')==str(ROOT)
 assert sha(ROOT/'config.toml')==CONFIG_SHA and sha(ROOT/'z_run.sh')==SCRIPT_SHA
 assert sha(ROOT/'geth.prev')==OLD_SHA
 result['new_start_ticks']=ticks(new_proc.pid);result['success']=True
 save('restart-result.json',result)
 print('RESTART_HEALTHY',json.dumps(result,separators=(',',':')),flush=True)
except Exception as error:
 result['success']=False;result['error']=str(error);result['failure_utc']=utc()
 print('RESTART_ERROR',str(error),flush=True)
 if stopped:
  if new_proc is not None and live(new_proc.pid):
   os.kill(new_proc.pid,signal.SIGINT);wait_exit(new_proc.pid)
  restore=ROOT/'.geth.rollback'
  assert not restore.exists()
  shutil.copy2(ROOT/'geth.prev',restore);os.replace(restore,ROOT/'geth')
  restored=launch();result['rollback_used']=True;result['rollback_pid']=restored.pid
  assert sha(ROOT/'geth')==OLD_SHA
  print('ROLLBACK_LAUNCHED',restored.pid,flush=True)
 save('restart-result.json',result)
 raise
