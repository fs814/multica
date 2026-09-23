from pathlib import Path
import subprocess,os,json,secrets,hashlib,socket,time,urllib.request,urllib.error,uuid
root=Path.cwd();ev=root/'.multica/evidence';db='tes85_p0_old_app_0916';owned=[];results={}
engine=['podman','exec','-i','multica-postgres-1']
def run(args,**kw):
 p=subprocess.run(args,capture_output=True,**kw)
 if p.returncode:raise RuntimeError(p.stderr.decode(errors='replace')[-1200:])
 return p.stdout
def sql(name,q):return run(engine+['psql','-X','-v','ON_ERROR_STOP=1','-U','multica','-d',name,'-At'],input=q.encode()).decode().strip()
def create(name):run(engine+['createdb','-U','multica',name]);owned.append(name)
def migrate(name,binary,cwd,label,direction='up'):
 env=dict(os.environ,DATABASE_URL=f'postgres://multica:multica@localhost:5432/{name}?sslmode=disable')
 env.pop('TES85_ALLOW_EMPTY_SCHEMA_ROLLBACK',None)
 with (ev/(label+'.log')).open('wb') as log:p=subprocess.run([str(ev/binary),direction],cwd=cwd,env=env,stdout=log,stderr=subprocess.STDOUT)
 return p.returncode
uid=str(uuid.uuid4());ws=str(uuid.uuid4());token='mul_'+secrets.token_hex(32)
class App:
 def __init__(self,name,label,binary='old-fork-server.exe',cwd=None):self.name=name;self.label=label;self.binary=binary;self.cwd=cwd or root/'.multica/old-build/server'
 def __enter__(self):
  with socket.socket() as sk:sk.bind(('127.0.0.1',0));port=sk.getsockname()[1]
  self.url=f'http://127.0.0.1:{port}'
  env={k:v for k,v in os.environ.items() if k.upper() in ['PATH','SYSTEMROOT','TEMP','TMP','USERPROFILE','LOCALAPPDATA','APPDATA']}
  env.update(DATABASE_URL=f'postgres://multica:multica@localhost:5432/{self.name}?sslmode=disable',PORT=str(port),APP_ENV='development',MULTICA_ENV_NAME='tes85-isolated-rehearsal')
  self.log=(ev/(self.label+'.log')).open('wb');self.p=subprocess.Popen([str(ev/self.binary)],cwd=self.cwd,env=env,stdout=self.log,stderr=subprocess.STDOUT)
  for _ in range(150):
   try:
    with urllib.request.urlopen(self.url+'/health',timeout=1) as r:health=json.load(r)
    results[self.label]={'health':health,'pid':self.p.pid};return self
   except Exception:
    if self.p.poll() is not None:break
    time.sleep(.2)
  self.__exit__(None,None,None);raise RuntimeError(self.label+' failed readiness')
 def request(self,method,path,body=None,want=200):
  data=json.dumps(body).encode() if body is not None else None
  req=urllib.request.Request(self.url+path,data=data,method=method,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json','X-Workspace-ID':ws})
  try:
   with urllib.request.urlopen(req,timeout=20) as r:code=r.status;out=json.load(r)
  except urllib.error.HTTPError as e:code=e.code;out=e.read().decode()
  if code!=want:raise RuntimeError(f'{method} {path}: {code} {out}')
  return out
 def __exit__(self,*args):
  if self.p.poll() is None:self.p.terminate()
  self.p.wait(timeout=20);self.log.close();results.setdefault(self.label,{})['stopped']=self.p.poll() is not None
try:
 create(db)
 assert migrate(db,'fork-migrate.exe',ev/'fork/server','old-app-baseline')==0
 sql(db,f'''INSERT INTO "user"(id,name,email) VALUES('{uid}','Rollback synthetic','rollback-{uid}@example.invalid');
 INSERT INTO workspace(id,name,slug) VALUES('{ws}','Rollback synthetic','rollback-{ws}');
 INSERT INTO member(workspace_id,user_id,role) VALUES('{ws}','{uid}','owner');
 INSERT INTO personal_access_token(user_id,name,token_hash,token_prefix) VALUES('{uid}','isolated test','{hashlib.sha256(token.encode()).hexdigest()}','synthetic');''')
 with App(db,'old-before') as app:
  tpl=app.request('POST','/api/workflow-templates',{'key':'rollback_fixture','name':'Rollback fixture','definition':{'schema_version':1,'entry_node':'end','nodes':[{'key':'end','type':'end'}]}},201)
  tid=tpl['id'];app.request('POST',f'/api/workflow-templates/{tid}/publish',{})
  first=app.request('POST',f'/api/workflow-templates/{tid}/run',{'title':'Before migration','description':'Synthetic old application business run','idempotency_key':'old-before'},201)
  assert first['status']=='completed';rid=first['id'];iid=first['issue_id']
  assert app.request('GET',f'/api/workflow-runs/{rid}')['id']==rid
  results['old-before']['business']={'template':tid,'run':rid,'status':first['status'],'issue':iid}
 vid=sql(db,f"SELECT id FROM workflow_template_version WHERE template_id='{tid}' AND status='published'")
 sql(db,f'''INSERT INTO workflow_input_instance(workspace_id,template_id,template_version_id,name,input,revision,archived_at,idempotency_key)
 SELECT '{ws}','{tid}','{vid}','Synthetic archived '||n,jsonb_build_object('fixture',n),3,now(),'tes85:old-app:synthetic:'||n FROM generate_series(1,8)n;
 INSERT INTO autopilot(workspace_id,title,assignee_id,status,created_by_type,created_by_id,workflow_template_id,workflow_template_version_id)
 SELECT '{ws}','Synthetic paused binding','{uid}','paused','member','{uid}','{tid}','{vid}' FROM workflow_input_instance WHERE workspace_id='{ws}' ORDER BY name LIMIT 1;''')
 def protected(name):return sql(name,f'''SELECT json_build_array((SELECT json_agg(t ORDER BY t.id) FROM workflow_input_instance t WHERE workspace_id='{ws}'),(SELECT json_agg(t ORDER BY t.id) FROM autopilot t WHERE workspace_id='{ws}'))::text''')
 before=protected(db)
 backup=run(engine+['pg_dump','-U','multica','-Fc',db]);results['backup_sha256']=hashlib.sha256(backup).hexdigest()
 assert migrate(db,'p0-final-migrate.exe',root/'.multica/p0/server','old-app-p0-up')==0
 assert protected(db)==before
 with App(db,'old-after-p0-restart') as app:
  assert app.request('GET',f'/api/workflow-runs/{rid}')['status']=='completed'
  assert app.request('GET',f'/api/workflow-templates/{tid}')['id']==tid
  changed=app.request('PUT',f'/api/issues/{iid}',{'description':'Updated by the fixed old version after P0 migration'})
  second=app.request('POST',f'/api/workflow-templates/{tid}/run',{'title':'After migration','description':'Second synthetic business run','idempotency_key':'old-after'},201)
  assert second['status']=='completed'
  results['old-after-p0-restart']['business']={'historical_run_read':True,'issue_updated':changed['id']==iid,'new_run_completed':True}
 assert protected(db)==before
 assert migrate(db,'p0-final-migrate.exe',root/'.multica/p0/server','old-app-protected-down','down')!=0
 assert protected(db)==before
 restored=db+'_restore';create(restored);run(engine+['pg_restore','-U','multica','-d',restored,'--exit-on-error'],input=backup)
 assert protected(restored)==before
 with App(restored,'old-after-backup-restore') as app:
  got=app.request('GET',f'/api/workflow-runs/{rid}');assert got['status']=='completed'
  assert app.request('GET',f'/api/issues/{iid}')['description']!='Updated by the fixed old version after P0 migration'
  results['old-after-backup-restore']['business']={'historical_run_read':True,'pre_migration_issue_restored':True}
 results['eight_synthetic_archived_instances_and_paused_binding_preserved']=True
 results['scope']='fixed old application business reads/writes after additive migration and backup restore; not new P1 service switching or real eight identity acceptance'
finally:
 for name in reversed(owned):run(engine+['dropdb','-U','multica',name])
 results['cleaned_databases']=owned
 (ev/'old-app-results.json').write_text(json.dumps(results,indent=2),encoding='utf-8')
print('old application rehearsal passed')
