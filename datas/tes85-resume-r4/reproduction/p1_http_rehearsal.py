from pathlib import Path
code=Path('.multica/evidence/old_app_rehearsal.py').read_text(encoding='utf-8')
exec(compile(code.split('try:\n create(db)')[0], 'rehearsal_helpers', 'exec'))
from concurrent.futures import ThreadPoolExecutor
results={};owned=[];db='tes85_p0_p1_http_0916'
def raw_request(app,body):
 request=urllib.request.Request(app.url+f'/api/workflow-templates/{tid}/run',data=json.dumps(body).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json','X-Workspace-ID':ws})
 try:
  with urllib.request.urlopen(request,timeout=30) as r:return r.status,json.load(r)
 except urllib.error.HTTPError as e:return e.code,json.loads(e.read())
try:
 create(db)
 assert migrate(db,'p1-migrate.exe',root/'.multica/p1-build/server','p1-http-schema')==0
 sql(db,f'''INSERT INTO "user"(id,name,email) VALUES('{uid}','HTTP synthetic','http-{uid}@example.invalid');
 INSERT INTO workspace(id,name,slug) VALUES('{ws}','HTTP synthetic','http-{ws}');
 INSERT INTO member(workspace_id,user_id,role) VALUES('{ws}','{uid}','owner');
 INSERT INTO personal_access_token(user_id,name,token_hash,token_prefix) VALUES('{uid}','isolated test','{hashlib.sha256(token.encode()).hexdigest()}','synthetic');''')
 with App(db,'p1-http','p1-server.exe',root/'.multica/p1-build/server') as app:
  tpl=app.request('POST','/api/workflow-templates',{'key':'published_fixture','name':'Published fixture','definition':{'schema_version':1,'entry_node':'end','nodes':[{'key':'end','type':'end'}]}},201)
  tid=tpl['id'];app.request('POST',f'/api/workflow-templates/{tid}/publish',{})
  body={'title':'Concurrent published run','description':'Deterministic real HTTP fixture','idempotency_key':'concurrent-create'}
  with ThreadPoolExecutor(max_workers=6) as ex:responses=list(ex.map(lambda _:raw_request(app,body),range(6)))
  assert all(code in [200,201] for code,_ in responses),responses
  ids={out['id'] for _,out in responses};assert len(ids)==1;rid=ids.pop()
  assert app.request('GET',f'/api/workflow-runs/{rid}')['status']=='completed'
  assert sql(db,f"SELECT count(*) FROM workflow_run WHERE template_id='{tid}'")=='1'
  assert sql(db,f"SELECT count(*) FROM issue i JOIN workflow_run r ON i.id=r.issue_id WHERE r.id='{rid}'")=='1'
  assert raw_request(app,dict(body,title='Conflicting payload'))[0]==409
  def counts():return sql(db,"SELECT json_build_array((SELECT count(*) FROM workflow_run),(SELECT count(*) FROM workflow_event),(SELECT count(*) FROM issue),(SELECT count(*) FROM agent_task_queue))::text")
  refusals={}
  for key,value in [('callback_destination_id',str(uuid.uuid4())),('input_instance_id',str(uuid.uuid4())),('execution_mode','draft_test')]:
   before=counts();status,out=raw_request(app,dict(body,idempotency_key='unsupported-'+key,**{key:value}));assert status==400,(key,status,out);assert counts()==before;refusals[key]={'status':status,'zero_side_effects':True}
  # The existing CLI accesses this isolated server with a synthetic fixture identity.
  env={k:v for k,v in os.environ.items() if k.upper() in ['PATH','SYSTEMROOT','TEMP','TMP','USERPROFILE','LOCALAPPDATA','APPDATA']}
  env.update(MULTICA_SERVER_URL=app.url,MULTICA_TOKEN=token,MULTICA_WORKSPACE_ID=ws)
  cli=subprocess.run([str(ev/'p1-multica.exe'),'issue','get',responses[0][1]['issue_id'],'--output','json'],cwd=root/'.multica/p1-build',env=env,capture_output=True)
  results['matched_cli']={'exit':cli.returncode}
  if cli.returncode==0:assert json.loads(cli.stdout)['id']==responses[0][1]['issue_id']
  else:(ev/'p1-cli-request-error.log').write_bytes(cli.stderr)
  results['p1-http']['published_execution']={'six_request_statuses':[c for c,_ in responses],'one_run_issue':True,'terminal_status':'completed','conflict_status':409,'unsupported':refusals}
  # One persisted run is shared with the real router; this fixture has no agent
  # node and does not claim an actual daemon/provider execution.
  results['coverage_limit']='real authenticated router/Engine/Notifier end-node flow; no actual daemon execution, intake route, handler full suite, or production switch'
finally:
 for name in reversed(owned):run(engine+['dropdb','-U','multica',name])
 results['cleaned_databases']=owned
 (ev/'p1-http-results.json').write_text(json.dumps(results,indent=2),encoding='utf-8')
print('P1 real HTTP rehearsal passed')
