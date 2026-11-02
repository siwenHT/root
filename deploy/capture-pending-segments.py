"""Read-only node/engine capture, reusing the 2026-10-23 collector."""
import collections,datetime,hashlib,json,os,pathlib,subprocess,sys,time,urllib.request
ROOT=pathlib.Path(sys.argv[1])
EXPECTED=sys.argv[2]
ROOT.mkdir(parents=True,exist_ok=False)
DURATION=300;DRAIN=45;WARMUP=10
def utc():return datetime.datetime.now(datetime.timezone.utc).isoformat()
def rpc(m,p=[]):
 req=urllib.request.Request('http://127.0.0.1:8547',data=json.dumps({'jsonrpc':'2.0','id':1,'method':m,'params':p}).encode(),headers={'Content-Type':'application/json'})
 x=json.load(urllib.request.urlopen(req,timeout=5))
 if'error'in x:raise RuntimeError(m+':'+str(x['error']))
 return x['result']
def sha(path):
 h=hashlib.sha256()
 with open(path,'rb')as f:
  for b in iter(lambda:f.read(1048576),b''):h.update(b)
 return h.hexdigest()
def identities():
 out=[]
 for d in pathlib.Path('/proc').iterdir():
  if not d.name.isdigit():continue
  try:
   n=(d/'comm').read_text().strip()
   if n in('geth','arb-engine'):
    out.append({'pid':int(d.name),'name':n,'start_ticks':(d/'stat').read_text().rsplit(') ',1)[1].split()[19],'exe':os.readlink(d/'exe'),'sha256':sha(d/'exe')})
  except OSError:pass
 return out
def snapshot():
 return {'utc':utc(),'processes':identities(),'files':{p:sha(p)for p in ['/home/bsc/fullnode/config.toml','/home/bsc/fullnode/z_run.sh','/home/bsc/codex_stage/arb-live/runtime.env']},
 'node_client_version':rpc('web3_clientVersion'),
 'engine_git':subprocess.check_output(['git','-C','/home/bsc/codex_stage/arb-engine-build','rev-parse','HEAD'],text=True).strip()}
before=snapshot()
assert EXPECTED[:8] in before['node_client_version']
(ROOT/'before.json').write_text(json.dumps(before,indent=2)+'\n')
keep={'pending_build_reaped','pending_ingress_classified','pending_scheduler_pass','pending_ws_received','received','head_received','pending_build_started','pending_parent_resolved','pending_next_head_retry','pending_build_rejected','pending_build_succeeded','pending_completed_stale','pending_connection_stale','pending_queue_expired','pending_queue_overflow','pending_no_routes','connection_open','connection_lost','heads_connection_open','heads_connection_lost'}
detail_keys={'execution_us','completion_wait_us','input_wait_us','classification_us','lane','ingest_us','retry_us','retry_count','retry_backlog','queue_depth','rpc_subscription','frame_received_unix_us','decoded_unix_us','decode_us','frame_bytes','generation','target_audit_sampled','head_age_ms','number','hash','block_time_ms','block_age_ms','block_sub_age_ms','scan_elapsed_ms','timing_block','retry','observed_head','inflight','ingress_us','queue_us','queue_wait_us','build_us','total_us','parent','parent_number','parent_time_ms','parent_age_ms','parent_lookup_us','remaining_ms','reason','previous_target_block','retries_remaining','target_block'}
evidence_keys={'deadline_parent_block','parent_time_ms','parent_age_ms','target_nonce','parent_nonce','state_nonce','transaction_nonce','reason','error_code'}
rust_out=(ROOT/'rust-stages.jsonl').open('w')
status_out=(ROOT/'runtime-status.jsonl').open('w')
node_out=(ROOT/'node-pages.jsonl').open('w')
health_out=(ROOT/'health.jsonl').open('w')
stage_counts=collections.Counter();parse_errors=0;audit_max=0;statuses=0;log_alerts=collections.Counter()
def stage_line(line):
 global parse_errors,audit_max
 try:r=json.loads(line)
 except ValueError:parse_errors+=1;return
 stage_counts[r.get('stage','unknown')]+=1;audit_max=max(audit_max,int(r.get('audit_dropped',0)))
 if r.get('stage')not in keep:return
 d=r.get('details',{})
 p={k:r[k]for k in ['audit_dropped','monotonic_us','observed_unix_us','session','stage','target_hash']if k in r}
 p['details']={k:v for k,v in d.items()if k in detail_keys}
 if isinstance(d.get('evidence'),dict):p['details']['evidence']={k:v for k,v in d['evidence'].items()if k in evidence_keys}
 rust_out.write(json.dumps(p,separators=(',',':'))+'\n')
def log_line(line):
 global statuses
 s=line.decode(errors='replace')
 if 'engine: live_status 'in s:
  try:
   r=json.loads(s.split('engine: live_status ',1)[1])
   p=r.get('pending',{})
   out={'observed_line_utc':s[:20],'capture_utc':utc(),**{k:r[k]for k in ['connected','paused','sent','audit_dropped','counters']if k in r},'pending':{k:v for k,v in p.items()if k!='builds_by_head'}}
   status_out.write(json.dumps(out,separators=(',',':'))+'\n');statuses+=1
  except ValueError:log_alerts['status_parse_error']+=1
 for key,pattern in {'producer_paused':'producer paused','producer_resumed':'producer resumed','wallet_reconciliation_busy':'wallet reconciliation busy','node_observation_interrupted':'node observation interrupted','panic':'panicked at','fatal':'fatal runtime error'}.items():
  if pattern in s:log_alerts[key]+=1
class Follow:
 def __init__(self,path,callback,tail=2097152):
  self.path=pathlib.Path(path);self.callback=callback;self.rotations=0;self.gaps=0;self.partial=b''
  self.f=self.path.open('rb');size=os.fstat(self.f.fileno()).st_size
  self.f.seek(max(0,size-tail))
  if size>tail:self.f.readline()
  self.read()
 def read(self):
  while True:
   data=self.f.read(1048576)
   if not data:break
   chunks=(self.partial+data).split(b'\n');self.partial=chunks.pop()
   for line in chunks:
    if line:self.callback(line)
 def poll(self):
  self.read()
  try:current=self.path.stat()
  except FileNotFoundError:return
  old=os.fstat(self.f.fileno())
  if current.st_ino!=old.st_ino:
   self.read()
   if self.partial:self.gaps+=1;self.partial=b''
   self.f.close();self.f=self.path.open('rb');self.rotations+=1;self.read()
  elif current.st_size<self.f.tell():
   self.gaps+=1;self.f.seek(0);self.partial=b'';self.read()
stages=Follow('/home/bsc/codex_stage/arb-live/state/diagnostics/stages-current.jsonl',stage_line)
logs=Follow('/home/bsc/codex_stage/arb-live/logs/arb-engine.current.log',log_line)
initial=rpc('debug_pendingTiming',['0x0','0x1'])
boot=initial['boot_id'];cursor=max(initial['oldest_sequence']-1,initial['latest_sequence']-20000,0)
pages=events=loss=rpc_errors=0;node_stages=collections.Counter()
meta={'capture_start_utc':utc(),'window_start_unix_us':time.time_ns()//1000+WARMUP*1000000,'window_seconds':DURATION,'drain_seconds':DRAIN,'boot_id':boot,'start_cursor':cursor,'initial_latest_sequence':initial['latest_sequence'],'node_sample_every':initial['sample_every'],'node_capacity':initial['capacity']}
start=time.monotonic();deadline=start+WARMUP+DURATION+DRAIN;next_health=start;next_progress=start+30;next_node=start
print('CAPTURE_STARTED',json.dumps(meta,separators=(',',':')),flush=True)
while time.monotonic()<deadline:
 now=time.monotonic()
 if now>=next_node:
  try:
   page=rpc('debug_pendingTiming',[hex(cursor),'0x400'])
   assert page['boot_id']==boot,'node restarted during capture'
   node_out.write(json.dumps(page,separators=(',',':'))+'\n')
   pages+=1;loss+=page['lost_since_cursor'];events+=len(page['events']);node_stages.update(e['stage']for e in page['events'])
   cursor=page['next_sequence'];next_node=now if cursor<page['latest_sequence']else now+.25
  except AssertionError:raise
  except Exception as err:rpc_errors+=1;next_node=now+.5;print('CAPTURE_RPC_ERROR',type(err).__name__,flush=True)
 stages.poll();logs.poll()
 if now>=next_health:
  try:
   health={'utc':utc(),'observed_unix_us':time.time_ns()//1000,'syncing':rpc('eth_syncing'),'head':int(rpc('eth_blockNumber'),16),'peers':int(rpc('net_peerCount'),16)}
   health_out.write(json.dumps(health,separators=(',',':'))+'\n')
  except Exception as err:
   health_out.write(json.dumps({'utc':utc(),'error_type':type(err).__name__})+'\n')
  next_health=now+15
 if now>=next_progress:
  for f in(node_out,rust_out,status_out,health_out):f.flush()
  print('CAPTURE_PROGRESS',json.dumps({'elapsed_seconds':round(now-start,1),'node_events':events,'node_cursor_loss':loss,'rust_rows_seen':sum(stage_counts.values()),'rust_parse_errors':parse_errors,'audit_dropped_max':audit_max,'stage_rotations':stages.rotations,'stage_gap_flags':stages.gaps,'status_records':statuses,'alerts':dict(log_alerts)},separators=(',',':')),flush=True)
  next_progress=now+30
 time.sleep(.02)
stages.poll();logs.poll()
for f in(node_out,rust_out,status_out,health_out):f.flush();f.close()
meta.update(capture_end_utc=utc(),window_end_unix_us=meta['window_start_unix_us']+DURATION*1000000,end_cursor=cursor,node_pages=pages,node_events=events,node_cursor_loss=loss,node_rpc_errors=rpc_errors,node_stage_counts=dict(node_stages),rust_stage_counts=dict(stage_counts),rust_parse_errors=parse_errors,rust_audit_dropped_max=audit_max,rust_stage_rotations=stages.rotations,rust_stage_gap_flags=stages.gaps,engine_log_rotations=logs.rotations,engine_log_gap_flags=logs.gaps,runtime_status_records=statuses,log_alert_counts=dict(log_alerts))
after=snapshot()
(ROOT/'after.json').write_text(json.dumps(after,indent=2)+'\n')
meta['identities_unchanged']=before['processes']==after['processes']
meta['config_hashes_unchanged']=before['files']==after['files']
(ROOT/'capture-meta.json').write_text(json.dumps(meta,indent=2)+'\n')
print('CAPTURE_COMPLETE',json.dumps(meta,separators=(',',':')),flush=True)
