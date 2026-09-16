from pathlib import Path
import subprocess, os, json, shutil, hashlib

root=Path.cwd(); evidence=root/'.multica/p0-evidence'
candidate=root/'.multica/p0/server'
old=Path('D:/sourcenew/ai/aiworkflow/multica/.multica/tes85-resume')
engine=['podman','exec','-i','multica-postgres-1']
results={}
owned=[]

def run(args, **kwargs):
    p=subprocess.run(args,capture_output=True,**kwargs)
    if p.returncode: raise RuntimeError(p.stderr.decode(errors='replace')[-3000:])
    return p.stdout

def sql(db, query):
    return run(engine+['psql','-X','-v','ON_ERROR_STOP=1','-U','multica','-d',db,'-At'],input=query.encode()).decode().strip()

def create(name):
    run(engine+['createdb','-U','multica',name]);owned.append(name)

def migrate(db,binary,cwd,label):
    env=dict(os.environ,DATABASE_URL=f'postgres://multica:multica@localhost:5432/{db}?sslmode=disable')
    with (evidence/(label+'.log')).open('wb') as f:
        p=subprocess.run([str(evidence/binary),'up'],cwd=cwd,env=env,stdout=f,stderr=subprocess.STDOUT)
    results[label]={'exit':p.returncode}
    print(label,p.returncode,flush=True)
    if p.returncode: raise RuntimeError((evidence/(label+'.log')).read_text(errors='replace')[-2400:])

def snapshot(db):
    tables=sql(db,"SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename<>'schema_migrations' ORDER BY tablename").splitlines()
    out={}
    for t in tables:
        value=sql(db,f'''SELECT count(*)::text || ':' || md5(COALESCE(string_agg(row_to_json(t)::text,E'\\n' ORDER BY row_to_json(t)::text),'')) FROM "{t}" t''')
        out[t]=value
    return out

baseline=evidence/'upstream'
(baseline/'migrations').mkdir(parents=True,exist_ok=True)
paths=run(['git','-C',str(old/'integration'),'ls-files','server/migrations']).decode().splitlines()
for name in paths:shutil.copyfile(old/'integration'/name,baseline/'migrations'/Path(name).name)

try:
    for mode in ['fresh','existing','fork']:
        db='tes85_p0_'+mode+'_20260916'
        try:
            create(db)
            if mode=='existing':
                migrate(db,'upstream-migrate.exe',baseline,'upstream-baseline')
                sql(db,'''INSERT INTO "user"(name,email) VALUES('TES85 synthetic','tes85-p0@example.invalid')''')
            if mode=='fork':
                migrate(db,'fork-migrate.exe',old/'repo/server','fork-baseline')
                sql(db,'''INSERT INTO "user"(name,email) VALUES('TES85 synthetic','tes85-p0@example.invalid');
                INSERT INTO workflow_template(workspace_id,key,name,status,created_by_type,created_by_id,archived_at)
                VALUES(gen_random_uuid(),'tes85-historical','Historical','archived','system',gen_random_uuid(),now());''')
                dump=run(engine+['pg_dump','-U','multica','-Fc',db])
                (evidence/'synthetic-fork.dump').write_bytes(dump)
                restored=db+'_restored';create(restored)
                run(engine+['pg_restore','-U','multica','-d',restored,'--exit-on-error'],input=dump)
                results['fork-restore']={'equal':snapshot(db)==snapshot(restored),'sha256':hashlib.sha256(dump).hexdigest()}
                db=restored
            before=snapshot(db)
            migrate(db,'p0-migrate.exe',candidate,mode+'-candidate')
            after=snapshot(db)
            results[mode+'-preservation']={'before':before,'after':after,'existing_rows_unchanged':all(after[k]==v for k,v in before.items())}
            migrate(db,'p0-migrate.exe',candidate,mode+'-repeat')
            results[mode+'-repeat']['rows_unchanged']=after==snapshot(db)
        except Exception as e:
            results[mode+'-failure']=str(e);print(mode,'FAILED',str(e)[-500:],flush=True)
        (evidence/'database-results.json').write_text(json.dumps(results,indent=2))
finally:
    # These names were created by this process, never inferred from live state.
    (evidence/'owned-databases.json').write_text(json.dumps(owned,indent=2))
