import re, sys

lines = open('refactor-diff.tmp', encoding='utf-8', errors='replace').read().splitlines()
minus = [l[1:].strip() for l in lines if l.startswith('-') and not l.startswith('---')]
plus = [l[1:].strip() for l in lines if l.startswith('+') and not l.startswith('+++')]

q = re.compile(r'"((?:[^"\\]|\\.)*)"|`([^`]*)`')

def literals(s):
    out = []
    for m in q.finditer(s):
        out.append(m.group(0))
    return out

pluset = {}
for p in plus:
    pluset.setdefault(p.lower(), []).append(p)

n = 0
for m in minus:
    for p in pluset.get(m.lower(), []):
        if m == p:
            continue
        om, op = literals(m), literals(p)
        if len(om) == len(op):
            for a, b in zip(om, op):
                if a.lower() == b.lower() and a != b:
                    print('LIT:', a, '->', b)
                    print('   OLD:', m[:140])
                    print('   NEW:', p[:140])
                    n += 1
print('literal case changes:', n)
