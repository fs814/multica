from pathlib import Path
import subprocess,os,json
root=Path.cwd();e=root/'.multica/p0-evidence';db='tes85_p0_fork_upstream_control_20260916'
prefix=['podman','exec','-i','multica-postgres-1']
subprocess.run(prefix+['createdb','-U','multica',db],check=True)
subprocess.run(prefix+['pg_restore','-U','multica','-d',db,'--exit-on-error'],input=(e/'synthetic-fork-rich.dump').read_bytes(),check=True)
env=dict(os.environ,DATABASE_URL=f'postgres://multica:multica@localhost:5432/{db}?sslmode=disable')
with (e/'fork-upstream-control.log').open('wb') as f:p=subprocess.run([str(e/'upstream-migrate.exe'),'up'],cwd=e/'upstream',env=env,stdout=f,stderr=subprocess.STDOUT)
query="SELECT COALESCE(json_object_agg(conrelid::regclass::text||'.'||conname,pg_get_constraintdef(oid)),'{}') FROM pg_constraint WHERE connamespace='public'::regnamespace"
def read(name):return json.loads(subprocess.check_output(prefix+['psql','-X','-U','multica','-d',name,'-At','-c',query]))
control=read(db);candidate=read('tes85_p0_fork_rich_20260916_restored');original=read('tes85_p0_fork_rich_20260916')
result={'upstream_exit':p.returncode,'candidate_constraints_equal_unmodified_upstream_upgrade':control==candidate,'unmodified_upstream_constraint_changes':{k:{'before':v,'after':control.get(k)} for k,v in original.items() if control.get(k)!=v}}
(e/'fork-upstream-control.json').write_text(json.dumps(result,indent=2))
print(json.dumps(result,indent=2))
