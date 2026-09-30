"""Tail the JSONL access log and bulk-index it into Elasticsearch. Stdlib only."""

import json
import os
import time
import urllib.request

ES = os.environ.get("ELASTIC_URL", "http://localhost:9200")
LOG = os.environ.get("LOG_FILE", "/logs/mockapi.jsonl")
INDEX = "mockapi-logs"


def bulk(lines):
    body = "".join('{"index":{"_index":"%s"}}\n%s\n' % (INDEX, line) for line in lines)
    req = urllib.request.Request(ES + "/_bulk", data=body.encode(),
                                 headers={"Content-Type": "application/x-ndjson"})
    urllib.request.urlopen(req, timeout=15).read()


def main():
    while not os.path.exists(LOG):
        time.sleep(1)
    pos = 0
    while True:
        with open(LOG, encoding="utf-8", errors="replace") as f:
            f.seek(pos)
            batch = [line.rstrip() for line in f if line.strip()][:500]
            pos = f.tell()
        if batch:
            try:
                bulk(batch)
            except Exception as e:
                print("bulk failed:", e, flush=True)
                pos -= sum(len(line.encode()) + 1 for line in batch)
        time.sleep(2)


if __name__ == "__main__":
    main()
