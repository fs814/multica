from pathlib import Path
import subprocess,hashlib,json
root=Path('.multica/p0');folder=root/'server/pkg/db/generated'
def hashes(): return {p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in folder.glob('*.go')}
before=hashes()
r=subprocess.run(['sqlc','generate','-f',str(root/'server/sqlc.yaml')],capture_output=True,text=True)
after=hashes()
Path('.multica/p0-evidence/generation.json').write_text(json.dumps({'sqlc':'v1.31.1','exit':r.returncode,'stderr':r.stderr,'stable':before==after,'generated_files':after},indent=2))
print('sqlc_exit',r.returncode,'stable',before==after)
assert r.returncode==0 and before==after
