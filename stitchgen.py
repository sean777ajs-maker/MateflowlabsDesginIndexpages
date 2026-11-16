#!/usr/bin/env python3
"""Generate one HTML page from Google Stitch and save it as
stitch-generated-<unixmillis>.html. Talks to the Stitch MCP server
(https://stitch.googleapis.com/mcp) directly over JSON-RPC with the
standard library only. No Node, no npm, no SDK.

Env: STITCH_API_KEY, PROMPT, ISSUE_NUMBER. Optional: STITCH_MCP_URL.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

MCP_URL = os.environ.get("STITCH_MCP_URL", "https://stitch.googleapis.com/mcp")


def fail(msg):
    print(f"stitchgen: {msg}", file=sys.stderr)
    sys.exit(1)


def parse_message(raw):
    text = raw.decode("utf-8", "replace").strip()
    if not text:
        fail("empty response from MCP server")
    if text.startswith("{"):
        return json.loads(text)
    last = None
    for line in text.splitlines():
        line = line.strip()
        if line.startswith("data:"):
            data = line[5:].strip()
            if data and data != "[DONE]":
                try:
                    last = json.loads(data)
                except json.JSONDecodeError:
                    pass
    if last is None:
        fail("no JSON-RPC message in SSE response")
    return last


def tool_text(result):
    return "".join(item.get("text", "") for item in result.get("content", [])
                   if isinstance(item, dict) and item.get("type") == "text")


class MCP:
    def __init__(self, url, key):
        self.url = url
        self.key = key
        self.session = None
        self.next_id = 0

    def _post(self, body, timeout=60):
        req = urllib.request.Request(self.url, data=json.dumps(body).encode(),
                                     headers={"Content-Type": "application/json",
                                              "Accept": "application/json, text/event-stream",
                                              "X-Goog-Api-Key": self.key})
        if self.session:
            req.add_header("Mcp-Session-Id", self.session)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                sid = resp.headers.get("Mcp-Session-Id")
                if sid:
                    self.session = sid
                return resp.read()
        except urllib.error.HTTPError as e:
            fail(f"POST {self.url}: HTTP {e.code}: {e.read()[:500]!r}")
        except urllib.error.URLError as e:
            fail(f"POST {self.url}: {e.reason}")

    def rpc(self, method, params=None, timeout=60):
        self.next_id += 1
        body = {"jsonrpc": "2.0", "id": self.next_id, "method": method}
        if params is not None:
            body["params"] = params
        msg = parse_message(self._post(body, timeout))
        if "error" in msg:
            fail(f"{method}: RPC error {msg['error']}")
        if "result" not in msg:
            fail(f"{method}: no result in response")
        return msg["result"]

    def notify(self, method):
        self._post({"jsonrpc": "2.0", "method": method})

    def call_tool(self, name, args, timeout=60):
        result = self.rpc("tools/call", {"name": name, "arguments": args}, timeout)
        if result.get("isError"):
            fail(f"tool {name} failed: {tool_text(result)}")
        if isinstance(result.get("structuredContent"), dict):
            return result["structuredContent"]
        text = tool_text(result)
        if not text:
            fail(f"tool {name} returned no content")
        try:
            return json.loads(text)
        except json.JSONDecodeError:
            fail(f"tool {name} returned non-JSON content: {text[:500]!r}")


def main():
    key = os.environ.get("STITCH_API_KEY") or fail("STITCH_API_KEY is not set")
    prompt = os.environ.get("PROMPT", "").replace("@stitch", "").strip()
    if not prompt:
        fail("PROMPT is empty (nothing to generate)")
    issue = os.environ.get("ISSUE_NUMBER") or fail("ISSUE_NUMBER is not set")

    mcp = MCP(MCP_URL, key)
    mcp.rpc("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                           "clientInfo": {"name": "stitchgen", "version": "0.1.0"}})
    mcp.notify("notifications/initialized")

    project = mcp.call_tool("create_project", {"title": f"MateflowLab-Issue-{issue}"})
    project_id = (project.get("name") or "").removeprefix("projects/") or project.get("projectId")
    if not project_id:
        fail(f"create_project: no project id in response: {project}")
    print(f"project: {project_id}")

    gen = mcp.call_tool("generate_screen_from_text",
                        {"projectId": project_id, "prompt": prompt}, timeout=900)
    screen = next((s for c in gen.get("outputComponents", [])
                   for s in (c.get("design") or {}).get("screens", [])), None)
    if not screen:
        fail("generate_screen_from_text: no screen in response")
    url = (screen.get("htmlCode") or {}).get("downloadUrl")
    if not url:
        name = screen.get("name", "")
        parts = name.strip("/").split("/")
        if len(parts) != 4:
            fail("generate response has no download URL and no screen name to look up")
        detail = mcp.call_tool("get_screen", {"name": name, "projectId": parts[1], "screenId": parts[3]})
        url = (detail.get("htmlCode") or {}).get("downloadUrl")
    if not url:
        fail("no HTML download URL for the generated screen")

    req = urllib.request.Request(url, headers={"X-Goog-Api-Key": key})
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            html = resp.read()
    except urllib.error.HTTPError as e:
        fail(f"download: HTTP {e.code}: {e.read()[:500]!r}")
    try:
        err = json.loads(html).get("error")
    except (ValueError, AttributeError):
        err = None
    if err:
        fail(f"download: server returned an error, not a page: {err.get('message')}")
    if not html.strip().startswith(b"<"):
        fail(f"downloaded content is not HTML (first bytes: {html[:100]!r})")
    filename = f"stitch-generated-{int(time.time() * 1000)}.html"
    with open(filename, "wb") as f:
        f.write(html)
    print(f"Saved HTML page from Google Stitch: {filename} ({len(html)} bytes)")


if __name__ == "__main__":
    main()
