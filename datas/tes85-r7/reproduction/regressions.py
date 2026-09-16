from pathlib import Path
import subprocess,os,json,re,datetime
root=Path.cwd(); c=root/'.multica/tes85-r7'; e=root/'.multica/tes85-r7-evidence'
url=re.search(r'defaultTestDatabaseURL = "([^"]+)"',(c/'server/cmd/migrate/migrate_concurrent_test.go').read_text())[1]
prefix=url.rsplit('/',1)[0]+'/'
db='tes85_p0_r7_tests_'+datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%d%H%M%S')
def env(name):return dict(os.environ,DATABASE_URL=prefix+name+'?sslmode=disable',TES85_RUN_MIGRATION_TESTS='1')
def sql(q):
 p=subprocess.run([str(e/'dbprobe.exe'),'exec'],input=q.encode(),env=env('postgres'),capture_output=True)
 if p.returncode:raise RuntimeError('database administration failed')
result={'database':db};owned=False
try:
 sql('CREATE DATABASE "'+db+'"');owned=True
 with (e/'migration-packages-final.jsonl').open('wb') as f:
  p=subprocess.run(['go','test','./internal/migrations','./cmd/migrate','-run','TestMigration|TestHistoricalMigration|TestTES85','-count=1','-json'],cwd=c/'server',env=env(db),stdout=f,stderr=subprocess.STDOUT)
 result['exit']=p.returncode;print('migration packages final exit',p.returncode)
 events=[json.loads(x) for x in (e/'migration-packages-final.jsonl').read_text().splitlines() if x.startswith('{')]
 result['tests']={}
 for pkg in sorted({x.get('Package') for x in events}):
  result['tests'][pkg]={action:len([x for x in events if x.get('Package')==pkg and x.get('Action')==action and x.get('Test') and '/' not in x['Test']]) for action in ['pass','fail','skip']}
 if p.returncode:raise RuntimeError('migration regression failed')
finally:
 if owned:sql('DROP DATABASE "'+db+'"');result['dropped']=True
 (e/'migration-packages-final.json').write_text(json.dumps(result,indent=2))
