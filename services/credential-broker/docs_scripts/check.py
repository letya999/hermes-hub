#!/usr/bin/env python3
"""Small offline memory-bank checks. Not a replacement for all markdownlint rules."""
from __future__ import annotations
import argparse
import datetime as dt
import hashlib
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SKIP = {'.git', '.build', '.tools', '__pycache__'}
FM = re.compile(r'\A---\ndescription: "([^"\n]+)"\nlast_verified: "(\d{4}-\d{2}-\d{2})"\n---\n')

def documents():
    return sorted(p for p in ROOT.rglob('*.md') if not SKIP.intersection(p.relative_to(ROOT).parts))

def info(p):
    text = p.read_text(encoding='utf-8')
    match = FM.match(text)
    if not match:
        raise ValueError(f'MD001 {p.relative_to(ROOT)}: expected description/last_verified, quoted')
    dt.date.fromisoformat(match[2])
    title = re.search(r'^# ([^\n]+)$', text[match.end():], re.M)
    if not title:
        raise ValueError(f'MD002 {p.relative_to(ROOT)}: missing H1')
    return text, match, title[1]

def expected_index(p):
    text, meta, title = info(p)
    rows = []
    for item in sorted(p.parent.iterdir(), key=lambda q: (q.is_file(), q.name)):
        if item == p or item.name.startswith('.'):
            continue
        target = item / 'index.md' if item.is_dir() else item
        if not target.exists() or target.suffix != '.md':
            continue
        _, child, name = info(target)
        desc = child[1].replace('|', '\\|')
        rows.append(f'| [{name}]({target.relative_to(p.parent).as_posix()}) | {desc} |')
    return text[:meta.end()] + f'\n# {title}\n\n| Документ | Когда открывать |\n| --- | --- |\n' + '\n'.join(rows) + ('\n' if rows else '')

def check_openapi():
    data = json.loads((ROOT/'api/openapi.json').read_text())
    errors, operations = [], set()
    def walk(node):
        if isinstance(node, dict):
            ref = node.get('$ref', '')
            if ref.startswith('#/'):
                value = data
                try:
                    for token in ref[2:].split('/'):
                        value = value[token.replace('~1', '/').replace('~0', '~')]
                except (KeyError, TypeError):
                    errors.append('API001 unresolved '+ref)
            op = node.get('operationId')
            if op:
                if op in operations: errors.append('API002 duplicate '+op)
                operations.add(op)
            for value in node.values(): walk(value)
        elif isinstance(node, list):
            for value in node: walk(value)
    walk(data)
    return errors

def check():
    errors = []
    for p in documents():
        try:
            text, meta, title = info(p)
            rel = str(p.relative_to(ROOT))
            body = text[meta.end():]
            # Do not interpret example shell/code fences as Markdown headings/links.
            prose = re.sub(r'^```[^\n]*\n.*?^```\s*$', '', body, flags=re.M|re.S)
            if len(re.findall(r'^# ', prose, re.M)) != 1:
                errors.append('MD003 '+rel+': exactly one H1')
            if not text.endswith('\n') or re.search(r'[ \t]+$', text, re.M) or '\t' in prose:
                errors.append('MD004 '+rel+': whitespace/newline')
            if '\n\n\n' in text: errors.append('MD005 '+rel+': repeated empty lines')
            if p.name == 'index.md' and text != expected_index(p):
                errors.append('MD006 '+rel+': index stale; run just docs-fix')
            for link in re.findall(r'\]\(([^)\n]+)\)', prose):
                target = link.split('#',1)[0]
                if not target or re.match(r'^[a-zA-Z]+:', target): continue
                dest = (p.parent/target).resolve()
                if not dest.is_relative_to(ROOT) or not dest.exists():
                    errors.append('MD007 '+rel+': broken local link '+target)
        except (ValueError, OSError) as e:
            errors.append(str(e))
    tests = '\n'.join(p.read_text() for p in ROOT.rglob('*_test.go') if not SKIP.intersection(p.parts))
    for p in (ROOT/'specs/active').glob('*.md'):
        if p.name == 'index.md': continue
        text=p.read_text()
        for section in ['Задача','Требования','Чего не делаем','Критерии приёмки']:
            if '## '+section not in text: errors.append('SP001 '+str(p)+': '+section)
        ids=set(re.findall(r'^### (R\d+)\b',text,re.M))
        accepted=set(re.findall(r'^\| (R\d+) \|',text,re.M))
        if not ids or ids != accepted: errors.append('SP002 requirement/acceptance mismatch '+str(p))
        for name in re.findall(r'`(Test[A-Za-z0-9_]+)`',text):
            if not re.search(r'func '+re.escape(name)+r'\(',tests): errors.append('SP003 no executable test '+name)
    for p in (ROOT/'.work').rglob('state.yaml'):
        try:
            d=json.loads(p.read_text()) # JSON is our deliberately restricted YAML 1.2 subset.
            required={'description','last_updated','done','current','next','evidence','review'}
            if set(d)!=required or set(d['review'])!={'open','resolved'}:
                errors.append('WK001 state schema '+str(p))
            dt.date.fromisoformat(d['last_updated'])
            for location in d['evidence']:
                if not (p.parent/location).exists(): errors.append('WK002 missing evidence '+location)
        except (ValueError, TypeError, KeyError) as e: errors.append('WK003 '+str(p)+': '+str(e))
    errors += check_openapi()
    lock=ROOT/'specs/baseline.json'
    if lock.exists():
        for file,digest in json.loads(lock.read_text()).items():
            if hashlib.sha256((ROOT/file).read_bytes()).hexdigest()!=digest:
                errors.append('SP004 frozen spec changed; new revision/ADR required: '+file)
    # CI job scripts are just entrypoints; tool setup is runner provisioning.
    for path in [ROOT/'.github/workflows/ci.yml',ROOT/'.gitlab-ci.yml']:
        if path.exists():
            for line in path.read_text().splitlines():
                if 'run:' in line and not re.search(r'run: just [a-z-]+$',line):
                    errors.append('CI001 only just recipes in job run steps '+str(path))
    if errors: raise SystemExit('\n'.join(errors))
    print(f'Documentation: {len(documents())} Markdown files; metadata, indexes, links, specs, states, OpenAPI references passed.')

def fix():
    indices=sorted((p for p in documents() if p.name=='index.md'),key=lambda p:len(p.parts),reverse=True)
    for p in indices: p.write_text(expected_index(p),encoding='utf-8')
    print(f'Generated {len(indices)} indexes.')

def new_change(name,title):
    if not re.fullmatch(r'[a-z][a-z0-9-]{0,59}',name) or not title.strip() or len(title)>120 or '\n' in title or '"' in title:
        raise SystemExit('Invalid change name/title')
    d=ROOT/'.work/todo'/name; d.mkdir(parents=True,exist_ok=False)
    today=dt.date.today().isoformat()
    (d/'plan.md').write_text(f'---\ndescription: "{title}"\nlast_verified: "{today}"\n---\n\n# {title}\n\n## Цель\n\n{title}. До реализации зафиксировать требования и проверяемые критерии.\n\n## Шаги\n\nПервый шаг: прочитать действующую спецификацию и границы доверия.\n\n## Приёмка\n\nЗапустить `just check`; результаты записать в state.yaml.\n')
    state=dict(description=title,last_updated=today,done=[],current='Уточнение требований и рисков',next=['Спецификация','Реализация','Проверки'],evidence=[],review=dict(open=[],resolved=[]))
    (d/'state.yaml').write_text(json.dumps(state,ensure_ascii=False,indent=2)+'\n')
    (d/'index.md').write_text(f'---\ndescription: "{title}"\nlast_verified: "{today}"\n---\n\n# {title}\n')
    fix()

def main():
    ap=argparse.ArgumentParser(description=__doc__);ap.add_argument('task',choices=['check','fix','new-change']);ap.add_argument('args',nargs='*');a=ap.parse_args()
    if a.task=='fix':fix()
    elif a.task=='check':check()
    elif len(a.args)==2:new_change(*a.args)
    else:raise SystemExit('new-change requires name and title')
if __name__=='__main__': main()
