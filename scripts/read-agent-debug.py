#!/usr/bin/env python3
"""Reassemble local agent debug JSON records from docker [compose] logs on stdin."""
import json
import sys

pending = {}
for line in sys.stdin:
    start = line.find("{")
    if start < 0:
        continue
    try:
        event = json.loads(line[start:])
    except json.JSONDecodeError:
        continue
    if "payload_id" not in event:
        print(json.dumps(event, ensure_ascii=False))
        continue
    index = event.get("chunk_index", 1)
    count = event.get("chunk_count", 1)
    if not isinstance(index, int) or not isinstance(count, int) or not 1 <= index <= count <= 1024:
        sys.exit("invalid debug log chunk metadata")
    key = (event.get("trace_id"), event.get("session_id"), event["payload_id"])
    group = pending.setdefault(key, {"event": event, "chunks": {}, "count": count})
    if group["count"] != count or index in group["chunks"]:
        sys.exit("inconsistent or duplicate debug log chunks")
    group["chunks"][index] = event.get("payload", "")
    if len(group["chunks"]) == count:
        merged = dict(group["event"])
        merged["payload"] = "".join(group["chunks"][n] for n in range(1, count + 1))
        merged.pop("chunk_index", None)
        merged.pop("chunk_count", None)
        print(json.dumps(merged, ensure_ascii=False))
        del pending[key]
if pending:
    sys.exit(f"incomplete debug log payloads: {len(pending)}; export a wider time range")
