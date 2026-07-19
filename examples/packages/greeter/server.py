"""Example MCP server shipped inside an apkg package (stdio JSON-RPC)."""
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
            "serverInfo": {"name": "greeter", "version": "1.0.0"},
        }})
    elif method == "notifications/initialized":
        continue
    elif method == "tools/list":
        send({"jsonrpc": "2.0", "id": req["id"], "result": {"tools": [{
            "name": "greet",
            "description": "Greets a person by name.",
            "inputSchema": {"type": "object", "properties": {
                "name": {"type": "string"}}, "required": ["name"]},
        }]}})
    elif method == "tools/call":
        params = req.get("params", {})
        name = params.get("arguments", {}).get("name", "world")
        send({"jsonrpc": "2.0", "id": req["id"], "result": {
            "content": [{"type": "text", "text": f"Greetings, {name}!"}],
        }})
    elif "id" in req:
        send({"jsonrpc": "2.0", "id": req["id"],
              "error": {"code": -32601, "message": "method not found"}})
