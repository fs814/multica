from pathlib import Path
import subprocess,os,json,hashlib,socket,time,urllib.request,shutil
root=Path.cwd();evidence=root/'.multica/p0-evidence';candidate=root/'.multica/p0/server'
old=Path('D:/sourcenew/ai/aiworkflow/multica/.multica/tes85-resume')
engine=['podman','exec','-i','multica-postgres-1'];results={};owned=[]
def run(args,**kwargs):
 p=subprocess.run(args,capture_output=True,**kwargs)
 if p.returncode:raise RuntimeError(p.stderr.decode(errors='replace')[-2000:])
 return p.stdout

def sql(db,query):return run(engine+['psql','-X','-v','ON_ERROR_STOP=1','-U','multica','-d',db,'-At'],input=query.encode()).decode().strip()
def create(db):run(engine+['createdb','-U','multica',db]);owned.append(db)
def snapshot(db):
 tables=sql(db,"SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename").splitlines()
 pieces=[f'''SELECT '{t}'::text AS name,count(*)::text || ':' || md5(COALESCE(string_agg(row_to_json(t)::text,E'\\n' ORDER BY row_to_json(t)::text),'')) AS digest FROM "{t}" t''' for t in tables]
 return json.loads(sql(db,'SELECT json_object_agg(name,digest) FROM ('+' UNION ALL '.join(pieces)+') data'))
def constraints(db):return json.loads(sql(db,"SELECT COALESCE(json_object_agg(conrelid::regclass::text||'.'||conname,pg_get_constraintdef(oid)),'{}') FROM pg_constraint WHERE connamespace='public'::regnamespace"))
def migrate(db,binary,cwd,label,direction='up'):
 env=dict(os.environ,DATABASE_URL=f'postgres://multica:multica@localhost:5432/{db}?sslmode=disable')
 with (evidence/(label+'.log')).open('wb') as f:p=subprocess.run([str(evidence/binary),direction],cwd=cwd,env=env,stdout=f,stderr=subprocess.STDOUT)
 results[label]={'exit':p.returncode};print(label,p.returncode,flush=True)
 return p.returncode

def health(db,binary,cwd,label):
 with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
 env={k:v for k,v in os.environ.items() if k.upper() in ['PATH','SYSTEMROOT','TEMP','TMP','USERPROFILE','LOCALAPPDATA','APPDATA']}
 env.update(DATABASE_URL=f'postgres://multica:multica@localhost:5432/{db}?sslmode=disable',PORT=str(port),APP_ENV='development',MULTICA_ENV_NAME='tes85-p0-rehearsal')
 with (evidence/(label+'.log')).open('wb') as f:
  p=subprocess.Popen([str(evidence/binary)],cwd=cwd,env=env,stdout=f,stderr=subprocess.STDOUT)
  try:
   for attempt in range(100):
    if p.poll() is not None:break
    try:
     with urllib.request.urlopen(f'http://127.0.0.1:{port}/health',timeout=1) as resp:
      result={'status':resp.status,'body':json.loads(resp.read())};results[label]=result;return
    except Exception:time.sleep(.2)
   results[label]={'failed':True,'exit':p.poll()}
  finally:
   if p.poll() is None:p.terminate()
   p.wait(timeout=15)

db='tes85_p0_fork_rich_20260916'
try:
 create(db)
 dump=(evidence/'synthetic-fork.dump').read_bytes()
 run(engine+['pg_restore','-U','multica','-d',db,'--exit-on-error'],input=dump)
 seed='''
 INSERT INTO workspace(id,name,slug) VALUES('10000000-0000-0000-0000-000000000001','TES85 synthetic','tes85-synthetic');
 INSERT INTO workflow_template(id,workspace_id,key,name,status,created_by_type,created_by_id,archived_at)
 VALUES('20000000-0000-0000-0000-000000000001','10000000-0000-0000-0000-000000000001','tes85-fixture','TES85 synthetic','archived','system','10000000-0000-0000-0000-000000000001',now());
 INSERT INTO workflow_template_version(id,workspace_id,template_id,version,definition,status,published_by_type,published_by_id,published_at)
 VALUES('30000000-0000-0000-0000-000000000001','10000000-0000-0000-0000-000000000001','20000000-0000-0000-0000-000000000001',1,'{}','published','system','10000000-0000-0000-0000-000000000001',now());
 INSERT INTO workflow_input_instance(id,workspace_id,template_id,template_version_id,name,input,revision,archived_at,idempotency_key)
 SELECT ('40000000-0000-0000-0000-'||lpad(n::text,12,'0'))::uuid,'10000000-0000-0000-0000-000000000001','20000000-0000-0000-0000-000000000001','30000000-0000-0000-0000-000000000001', 'Synthetic archived '||n,jsonb_build_object('fixture',n),3,now(),'tes85:synthetic:'||n FROM generate_series(1,8)n;
 INSERT INTO workflow_run(workspace_id,template_id,template_version_id,source,idempotency_key,status,completed_at,input_instance_id,input_instance_revision,input_instance_name,input_source)
 SELECT workspace_id,template_id,template_version_id,'manual','tes85:run:'||id::text,'completed',now(),id,2,name,'instance' FROM workflow_input_instance WHERE workspace_id='10000000-0000-0000-0000-000000000001';
 INSERT INTO autopilot(workspace_id,title,assignee_id,status,created_by_type,created_by_id,workflow_input_instance_id,workflow_input_instance_revision)
 VALUES('10000000-0000-0000-0000-000000000001','TES85 historical binding','10000000-0000-0000-0000-000000000001','paused','member','10000000-0000-0000-0000-000000000001','40000000-0000-0000-0000-000000000001',2);
 '''
 sql(db,seed)
 before=snapshot(db);before_constraints=constraints(db)
 richdump=run(engine+['pg_dump','-U','multica','-Fc',db]);(evidence/'synthetic-fork-rich.dump').write_bytes(richdump)
 restored=db+'_restored';create(restored)
 run(engine+['pg_restore','-U','multica','-d',restored,'--exit-on-error'],input=richdump)
 results['rich-backup-restore']={'all_rows_and_ledger_equal':before==snapshot(restored),'constraints_equal':before_constraints==constraints(restored),'sha256':hashlib.sha256(richdump).hexdigest()}
 db=restored
 code=migrate(db,'p0-migrate-final.exe',candidate,'rich-fork-final-up')
 after=snapshot(db);after_constraints=constraints(db)
 results['rich-fork-preservation']={'before':before,'after':after,'rows_equal_except_ledger':all(after[k]==v for k,v in before.items() if k!='schema_migrations'),'old_constraints_preserved':all(after_constraints.get(k)==v for k,v in before_constraints.items())}
 if code==0:
  migrate(db,'p0-migrate-final.exe',candidate,'rich-fork-final-repeat')
  results['rich-fork-final-repeat']['all_rows_and_ledger_equal']=after==snapshot(db)
  down_code=migrate(db,'p0-migrate-final.exe',candidate,'rich-fork-protected-down','down')
  results['rich-fork-protected-down']['refused_before_any_change']=down_code!=0 and after==snapshot(db) and after_constraints==constraints(db)
  runroot=evidence/'old-fork';(runroot/'migrations').mkdir(parents=True,exist_ok=True)
  for p in (old/'repo/server/migrations').glob('*.sql'):shutil.copyfile(p,runroot/'migrations'/p.name)
  health(db,'fork-server.exe',runroot,'old-fork-retained-schema-health')
 else:
  results['gate']='P0 FAILED; P1 not authorized to start until compatibility scope is resolved'
 for mode in ['fresh','existing']:
  target='tes85_p0_'+mode+'_20260916';before=snapshot(target)
  migrate(target,'p0-migrate-final.exe',candidate,mode+'-final-repeat')
  results[mode+'-final-repeat']['all_rows_and_ledger_equal']=before==snapshot(target)
  health(target,'upstream-server.exe',evidence/'upstream','old-upstream-'+mode+'-health')
except Exception as e:
 results['failure']=str(e);print('FAILED',str(e)[-1000:],flush=True)
finally:
 (evidence/'final-database-results.json').write_text(json.dumps(results,indent=2))
 (evidence/'owned-final-databases.json').write_text(json.dumps(owned,indent=2))
 print(json.dumps({k:v for k,v in results.items() if k!='rich-fork-preservation'},indent=2),flush=True)
