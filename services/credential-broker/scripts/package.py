#!/usr/bin/env python3
"""Source-only deterministic archive, with per-file hashes and no runtime state."""
import argparse
import hashlib
import json
import stat
import zipfile
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
SKIP={'.git','.build','.tools','__pycache__','.pytest_cache'}
SUFFIX={'.go','.mod','.md','.json','.yaml','.yml','.py','.conf','.toml','.txt','.jsonl','.out','.service','.sh'}
NAMES={'go.mod','go.sum','justfile','LICENSE','Dockerfile','.gitignore','.editorconfig','.dockerignore','pre-commit'}

def collect():
    files=[]
    for p in sorted(ROOT.rglob('*')):
        rel=p.relative_to(ROOT)
        if SKIP.intersection(rel.parts):continue
        if p.is_symlink():raise SystemExit('Symlink in source tree: '+str(rel))
        if not p.is_file():continue
        if p.suffix in {'.private','.key','.p12','.pfx'} or p.name in {'.env','credentials.env','ledger.bin','runtime_session.json'}:
            raise SystemExit('Forbidden runtime artifact: '+str(rel))
        if p.suffix not in SUFFIX and p.name not in NAMES:raise SystemExit('Unreviewed archive file type: '+str(rel))
        files.append(p)
    return files

def main():
    ap=argparse.ArgumentParser();ap.add_argument('--output',type=Path,default=ROOT/'.build/dist/credential-broker.zip');a=ap.parse_args()
    # Evidence is required, not fabricated by the packager.
    ev=ROOT/'.work/done/CHG-001-credential-broker/artifacts'
    for name in ['verification.md','coverage.json','tests.jsonl']:
        if not (ev/name).is_file():raise SystemExit('Missing verified evidence: '+name)
    files=collect();manifest=[]
    a.output.parent.mkdir(parents=True,exist_ok=True)
    def write(z,path,data,executable=False):
        item=zipfile.ZipInfo('credential-broker/'+path,date_time=(2026,9,18,0,0,0))
        item.compress_type=zipfile.ZIP_DEFLATED
        item.external_attr=((stat.S_IFREG | (0o755 if executable else 0o644))<<16)
        z.writestr(item,data)
    with zipfile.ZipFile(a.output,'w',compression=zipfile.ZIP_DEFLATED,compresslevel=9) as z:
        for p in files:
            name=p.relative_to(ROOT).as_posix();raw=p.read_bytes();manifest.append(hashlib.sha256(raw).hexdigest()+'  '+name)
            write(z,name,raw,p.name=='pre-commit' or p.suffix=='.sh')
        write(z,'MANIFEST.sha256',('\n'.join(manifest)+'\n').encode())
    digest=hashlib.sha256(a.output.read_bytes()).hexdigest()
    print(json.dumps({'archive':str(a.output),'files':len(files)+1,'bytes':a.output.stat().st_size,'sha256':digest},indent=2))
if __name__=='__main__':main()
