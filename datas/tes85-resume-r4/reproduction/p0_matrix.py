from pathlib import Path
import subprocess, json, hashlib, os, zipfile
root=Path(__file__).resolve().parent
repo=root.parent/'p0'
pkg=root/'package'
def run(args, cwd=None, env=None, data=None):
    p=subprocess.run(args,cwd=cwd,env=env,input=data,capture_output=True)
    if p.returncode: raise RuntimeError(str(args)+': '+p.stderr.decode(errors='replace')[-2000:])
    return p.stdout
for kind,rev in [('upstream','9d18186e6e8cfa168d6053d33d652e86fadfc12b'),('fork','e44b6a8d70b826ff368390cab62de5bbffb7167e')]:
    archive=root/(kind+'.zip')
    run(['git','archive','--format=zip','-o',str(archive),rev,'server'],cwd=repo)
    with zipfile.ZipFile(archive) as z:z.extractall(root/kind)
    run(['go','build','-o',str(root/(kind+'-migrate.exe')),'./cmd/migrate'],cwd=root/kind/'server')
    print(kind,'built',flush=True)
run(['go','build','-o',str(root/'p0-migrate.exe'),'./cmd/migrate'],cwd=repo/'server')
run(['go','build','./pkg/db/generated'],cwd=repo/'server')
run(['go','vet','./cmd/migrate','./pkg/db/generated'],cwd=repo/'server')
before={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in (repo/'server/pkg/db/generated').glob('*') if p.is_file()}
run(['sqlc','generate'],cwd=repo/'server')
after={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in (repo/'server/pkg/db/generated').glob('*') if p.is_file()}
run(['sqlc','generate'],cwd=repo/'server')
again={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in (repo/'server/pkg/db/generated').glob('*') if p.is_file()}
assert after==again
assert not run(['git','diff','--ignore-space-at-eol','--','server/pkg/db/generated'],cwd=repo)
print('build/vet/sqlc stable passed',flush=True)
engine=['podman','exec','-i','multica-postgres-1']
def sql(db,q):return run(engine+['psql','-X','-v','ON_ERROR_STOP=1','-U','multica','-d',db,'-At'],data=q.encode()).decode().strip()
def snap(db):
    tables=sql(db,"SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename").splitlines()
    if not tables:return {}
    parts=[f'''SELECT '{t}'::text name, count(*)::text || ':' || md5(COALESCE(string_agg(row_to_json(t)::text,E'\\n' ORDER BY row_to_json(t)::text),'')) digest FROM "{t}" t''' for t in tables]
    return json.loads(sql(db,'SELECT json_object_agg(name,digest) FROM ('+' UNION ALL '.join(parts)+') x'))
def cons(db):return json.loads(sql(db,"SELECT COALESCE(json_object_agg(conrelid::regclass::text||'.'||conname,pg_get_constraintdef(oid)),'{}') FROM pg_constraint WHERE connamespace='public'::regnamespace"))
def migrate(db,kind,label,direction='up'):
    env=dict(os.environ,DATABASE_URL=f'postgres://multica:multica@localhost:5432/{db}?sslmode=disable')
    env.pop('TES85_ALLOW_EMPTY_SCHEMA_ROLLBACK',None)
    cwd=repo/'server' if kind=='p0' else root/kind/'server'
    with (root/(label+'.log')).open('wb') as f:p=subprocess.run([str(root/(kind+'-migrate.exe')),direction],cwd=cwd,env=env,stdout=f,stderr=subprocess.STDOUT)
    return p.returncode
results={};owned=[]
def create(name):
    assert name.startswith('tes85_p0_fix_matrix_')
    run(engine+['createdb','-U','multica',name]);owned.append(name)
try:
    for mode in ['fresh','existing','fork']:
        db='tes85_p0_fix_matrix_'+mode+'_0916';create(db)
        if mode!='fresh':
            assert migrate(db,'upstream' if mode=='existing' else 'fork',mode+'-baseline')==0
            sql(db,'''INSERT INTO "user"(name,email) VALUES('Review synthetic','review@example.invalid')''')
        if mode=='fork':
            sql(db,"INSERT INTO workflow_template(workspace_id,key,name,status,created_by_type,created_by_id,archived_at) VALUES(gen_random_uuid(),'review-fixture','Synthetic','archived','system',gen_random_uuid(),now())")
            backup=run(engine+['pg_dump','-U','multica','-Fc',db])
            restored=db+'_restore';create(restored)
            run(engine+['pg_restore','-U','multica','-d',restored,'--exit-on-error'],data=backup)
            assert snap(db)==snap(restored) and cons(db)==cons(restored)
            db=restored
        before=snap(db);bc=cons(db)
        assert migrate(db,'p0',mode+'-up')==0
        after=snap(db);ac=cons(db)
        assert all(after[k]==v for k,v in before.items() if k!='schema_migrations')
        assert migrate(db,'p0',mode+'-repeat')==0
        assert snap(db)==after
        assert migrate(db,'p0',mode+'-down','down')!=0
        assert snap(db)==after and cons(db)==ac
        results[mode]={'business_rows_preserved':True,'repeat_equal':True,'down_refused_without_change':True,'constraint_diff':{k:{'before':v,'after':ac.get(k)} for k,v in bc.items() if ac.get(k)!=v}}
        if mode=='fork':
            control='tes85_p0_fix_matrix_control_0916';create(control)
            run(engine+['pg_restore','-U','multica','-d',control,'--exit-on-error'],data=backup)
            assert migrate(control,'upstream','fork-control')==0
            # Candidate adds no constraints to the already complete fork.
            assert cons(control)==ac
            results[mode]['constraints_equal_upstream_control']=True
        print(mode,'PASS',flush=True)
finally:
    for db in reversed(owned):
        run(engine+['dropdb','-U','multica',db])
    results['cleaned_databases']=owned
    (root/'review-results.json').write_text(json.dumps(results,indent=2))
