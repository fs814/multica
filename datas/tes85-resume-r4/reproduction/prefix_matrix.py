from pathlib import Path
import os,subprocess,json
root=Path.cwd();ev=root/'.multica/evidence';repo=root/'.multica/p0'
r={}
for name in ['tes85_p0_prefix_0916','tes85Xp0Xprefix0916','tes85Xp0_prefix0916','tes85_p0Xprefix0916','tes85p0prefix0916','tes85ordinaryprefix0916']:
 subprocess.run(['podman','exec','multica-postgres-1','createdb','-U','multica',name],check=True)
 try:
  env=dict(os.environ,DATABASE_URL=f'postgres://multica:multica@localhost:5432/{name}?sslmode=disable',TES85_RUN_MIGRATION_TESTS='1')
  p=subprocess.run(['go','test','./cmd/migrate','-run','^TestTES85DatabasePrefixBoundary$','-count=1','-json'],cwd=repo/'server',env=env,capture_output=True)
  (ev/(name+'.jsonl')).write_bytes(p.stdout+p.stderr);r[name]=p.returncode
 finally: subprocess.run(['podman','exec','multica-postgres-1','dropdb','-U','multica',name],check=True)
(ev/'prefix-results.json').write_text(json.dumps(r,indent=2));assert all(v==0 for v in r.values()),r
