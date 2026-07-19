"""Minimal MCP server over stdio (newline-delimited JSON-RPC) for tests."""
import json
import sys


def send(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    req = json.loads(line)
    method = req.get("method")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": req["id"], "result": {
            "protocolVersion": "2024-11-05",
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "mock", "version": "0.0.1"},
        }})
    elif method == "notifications/initialized":
        continue
    elif method == "tools/list":
        send({"jsonrpc": "2.0", "id": req["id"], "result": {"tools": [{
            "name": "echo",
            "description": "Echoes its arguments back as text.",
            "inputSchema": {"type": "object"},
        }]}})
    elif method == "tools/call":
        params = req.get("params", {})
        if params.get("name") != "echo":
            send({"jsonrpc": "2.0", "id": req["id"],
                  "error": {"code": -32602, "message": "unknown tool"}})
            continue
        send({"jsonrpc": "2.0", "id": req["id"], "result": {
            "content": [{"type": "text",
                         "text": json.dumps(params.get("arguments", {}))}],
        }})
    else:
        if "id" in req:
            send({"jsonrpc": "2.0", "id": req["id"],
                  "error": {"code": -32601, "message": "method not found"}})
