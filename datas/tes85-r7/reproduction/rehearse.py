from pathlib import Path
import subprocess, os, json, re, hashlib, zipfile, datetime
ROOT=Path.cwd(); candidate=ROOT/'.multica/tes85-r7'; evidence=ROOT/'.multica/tes85-r7-evidence'
base='fa698dcfc5f143a5c5c372944dbafb2b2ae2577b'
url=re.search(r'defaultTestDatabaseURL = "([^"]+)"',(candidate/'server/cmd/migrate/migrate_concurrent_test.go').read_text())[1]
prefix=url.rsplit('/',1)[0]+'/'
owned=[]; result={'apply_allowed':False,'baseline':base,'candidate':subprocess.check_output(['git','rev-parse','HEAD'],cwd=candidate,text=True).strip()}
def env(db):
 return dict(os.environ,DATABASE_URL=prefix+db+'?sslmode=disable',TES85_RUN_MIGRATION_TESTS='1')
def sql(db,q,exec=False):
 p=subprocess.run([str(evidence/'dbprobe.exe')]+(['exec'] if exec else []),input=q.encode(),capture_output=True,env=env(db))
 if p.returncode: raise RuntimeError('database probe failed: '+p.stderr.decode(errors='replace')[-1500:])
 return p.stdout.decode() if exec else json.loads(p.stdout)
def create(db):
 assert re.fullmatch('tes85_r7_[a-z0-9_]+',db)
 sql('postgres','CREATE DATABASE "'+db+'"',True);owned.append(db)
def run(label,args,cwd,db=None):
 with (evidence/(label+'.log')).open('wb') as f:
  p=subprocess.run(args,cwd=cwd,env=env(db) if db else None,stdout=f,stderr=subprocess.STDOUT)
 result[label]={'exit':p.returncode};print(label,p.returncode,flush=True)
 if p.returncode:raise RuntimeError(label+' failed; inspect evidence')
def snapshot(db):
 tables=[r[0] for r in sql(db,"SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename")]
 pieces=[f'''SELECT '{t}'::text name,count(*)::text||':'||md5(COALESCE(string_agg(row_to_json(t)::text,E'\\n' ORDER BY row_to_json(t)::text),'')) digest FROM "{t}" t''' for t in tables]
 return dict(sql(db,' UNION ALL '.join(pieces)))
def ledger(db): return sql(db,'SELECT version,applied_at::text FROM schema_migrations ORDER BY version')
def catalog(db): return sql(db,"SELECT c.oid::text,c.relname,c.relkind::text,COALESCE(obj_description(c.oid,'pg_class'),'') FROM pg_class c WHERE c.relnamespace='public'::regnamespace ORDER BY c.oid")
def sentinels(db):
 return sql(db,"SELECT id::text,name,slug FROM workspace WHERE id='70000000-0000-0000-0000-000000000001'"), sql(db,"SELECT id::text,workspace_id::text,key,name,status,created_by_type,created_by_id::text,archived_at::text FROM workflow_template WHERE id='70000000-0000-0000-0000-000000000002'")
def seed(db):
 sql(db,"INSERT INTO workspace(id,name,slug) VALUES('70000000-0000-0000-0000-000000000001','R7 synthetic upgrade sentinel','tes85-r7-synthetic'); INSERT INTO workflow_template(id,workspace_id,key,name,status,created_by_type,created_by_id,archived_at) VALUES('70000000-0000-0000-0000-000000000002','70000000-0000-0000-0000-000000000001','r7-sentinel','R7 sentinel','archived','system','70000000-0000-0000-0000-000000000001',now());",True)
try:
 result['server_identity']=sql('postgres',"SELECT current_database(),current_setting('server_version'),system_identifier::text FROM pg_control_system()")
 # Use the previously delivered clean R6 executable, never a product binary.
 with zipfile.ZipFile(ROOT/'datas/tes85-r6/TES-85-r6-windows-amd64.zip') as z:
  (evidence/'r6-migrate.exe').write_bytes(z.read('migrate.exe'))
 meta=subprocess.check_output(['go','version','-m',str(evidence/'r6-migrate.exe')],text=True)
 assert 'vcs.revision='+base in meta and 'vcs.modified=false' in meta
 (evidence/'r6-migrate.build.txt').write_text(meta)
 result['r6_binary_sha256']=hashlib.sha256((evidence/'r6-migrate.exe').read_bytes()).hexdigest()
 frozen=json.loads((ROOT/'datas/tes85-r6/proposal/migration-collisions.json').read_text(encoding='utf-8-sig'))
 frozen_stems=sorted(s for group in frozen.values() for s in group)
 partial=evidence/'r6-through-255';(partial/'migrations').mkdir(parents=True,exist_ok=True)
 for f in (candidate/'server/migrations').glob('*.sql'):
  if int(f.name.split('_')[0])<=255:(partial/'migrations'/f.name).write_bytes(f.read_bytes())
 stamp=datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%d%H%M%S')
 for mode in ['partial','existing','fresh']:
  db='tes85_r7_'+mode+'_'+stamp;create(db)
  if mode!='fresh':
   run(mode+'-r6-up',[str(evidence/'r6-migrate.exe'),'up'],partial if mode=='partial' else candidate/'server',db)
   seed(db);before= snapshot(db);old=ledger(db);cat=catalog(db);sentinel=sentinels(db)
  run(mode+'-r7-up',[str(evidence/'r7-migrate.exe'),'up'],candidate/'server',db)
  after=snapshot(db);new=ledger(db)
  applied={r[0] for r in new}
  assert all(s in applied for s in frozen_stems)
  result[mode+'-ledger']={'database':db,'count':len(new),'frozen_groups':25,'frozen_stems_recorded':len(frozen_stems)}
  if mode!='fresh':
   assert set(map(tuple,old))<=set(map(tuple,new))
   assert sentinel==sentinels(db)
   result[mode+'-ledger'].update(old_count=len(old),old_entries_and_timestamps_preserved=True,synthetic_rows_preserved=True)
   if mode=='existing':
    assert before==after and cat==catalog(db)
    result[mode+'-ledger'].update(all_public_table_rows_and_ledger_equal=True,catalog_equal=True)
  cat=catalog(db)
  run(mode+'-r7-rerun',[str(evidence/'r7-migrate.exe'),'up'],candidate/'server',db)
  assert after==snapshot(db) and new==ledger(db) and cat==catalog(db)
  result[mode+'-ledger']['rerun_all_rows_ledger_catalog_equal']=True
except Exception as e:
 result['failure']=str(e);raise
finally:
 result['cleanup']=[]
 for db in owned:
  try:
   sql('postgres','DROP DATABASE "'+db+'"',True)
   result['cleanup'].append({'database':db,'dropped':True})
  except Exception as e:result['cleanup'].append({'database':db,'dropped':False,'error':str(e)})
 (evidence/'database-results.json').write_text(json.dumps(result,indent=2),encoding='utf-8')
