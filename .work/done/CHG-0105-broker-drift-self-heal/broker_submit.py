"""Submit a credential through the live Credential Broker connect form.

Usage: python broker_submit.py <request_id> <FIELD=VALUE> [FIELD=VALUE...]
Dev stack runs BROKER_DIRECT_FORM=1 (pre-approved sessions) and self-signed TLS.
"""
import http.cookiejar
import re
import ssl
import sys
import urllib.request
import uuid

BASE = "https://127.0.0.1:8787"


def main():
    rid = sys.argv[1]
    fields = dict(kv.split("=", 1) for kv in sys.argv[2:])
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(
        urllib.request.HTTPSHandler(context=ctx),
        urllib.request.HTTPCookieProcessor(jar))

    page = opener.open(BASE + "/connect/" + rid, timeout=30).read().decode()
    m = re.search(r'name="csrf" value="([^"]+)"', page)
    if not m:
        print(page[:800]); sys.exit("no csrf — session not approved or request dead")
    csrf = m.group(1)
    want = set(re.findall(r'name="(f_[^"]+|[A-Z_]+)"', page)) - {"csrf"}
    missing = want - set(fields)
    if missing:
        sys.exit("form wants fields not supplied: " + ",".join(sorted(missing)))

    boundary = "----replay" + uuid.uuid4().hex
    body = b""
    for name, value in [("csrf", csrf)] + sorted(fields.items()):
        body += ("--%s\r\nContent-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n"
                 % (boundary, name, value)).encode()
    body += ("--%s--\r\n" % boundary).encode()
    req = urllib.request.Request(BASE + "/connect/" + rid + "/submit", data=body, method="POST")
    req.add_header("Content-Type", "multipart/form-data; boundary=" + boundary)
    res = opener.open(req, timeout=30)
    html = res.read().decode()
    print(res.status, "accepted" if "приняты" in html else html[:400])


if __name__ == "__main__":
    main()
