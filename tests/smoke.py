"""Сквозной прогон Windows-сборки в одноразовом профиле. Данные задач остаются в памяти."""

import argparse
import concurrent.futures
import contextlib
import ctypes
import http.cookiejar
import json
import os
import pathlib
import queue
import re
import secrets
import statistics
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOOLS = {"jira_search_text", "jira_search_jql", "jira_get_issue", "jira_get_all_fields"}
TOKEN = secrets.token_hex(24)


class RPC:
    def __init__(self, command, env):
        self.p = subprocess.Popen(
            command,
            env=env,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            creationflags=subprocess.CREATE_NO_WINDOW,
        )
        self.messages = queue.Queue()
        self.counter = 0
        threading.Thread(target=self.read, daemon=True).start()

    def read(self):
        for line in self.p.stdout:
            self.messages.put(json.loads(line))
        self.messages.put(None)

    def send(self, message):
        self.p.stdin.write(json.dumps({"jsonrpc": "2.0", **message}).encode() + b"\n")
        self.p.stdin.flush()

    def call(self, method, params):
        self.counter += 1
        self.send({"id": self.counter, "method": method, "params": params})
        while True:
            msg = self.messages.get(timeout=60)
            if msg is None:
                raise RuntimeError("MCP-клиент закрыл поток")
            if msg.get("id") == self.counter and "method" not in msg:
                if "error" in msg:
                    raise RuntimeError(method + " — " + str(msg["error"].get("code")))
                return msg["result"]

    def init(self):
        result = self.call(
            "initialize",
            {
                "protocolVersion": "2025-11-25",
                "capabilities": {},
                "clientInfo": {"name": "engmcp-smoke", "version": "1"},
            },
        )
        assert result["serverInfo"]["name"] == "engmcp"
        self.send({"method": "notifications/initialized"})

    def tools(self):
        return {x["name"] for x in self.call("tools/list", {})["tools"]}

    def tool(self, name, args):
        return self.call("tools/call", {"name": name, "arguments": args})

    def close(self):
        self.p.stdin.close()
        try:
            self.p.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.p.kill()
            self.p.wait()
        self.p.stdout.close()


class Browser:
    def __init__(self, exe, env):
        link = (
            subprocess.check_output([exe, "panel-url"], env=env, timeout=10)
            .decode()
            .strip()
        )
        address, token = link.split("#")
        self.origin = address.rstrip("/")
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),
        )
        response = self.post(self.origin + "/login", {"ticket": token})
        self.url = response.url
        self.refresh(response)
        try:
            self.post(self.origin + "/login", {"ticket": token})
        except urllib.error.HTTPError as error:
            assert error.code == 403
        else:
            raise AssertionError("Повторный вход по одноразовой ссылке")

    def refresh(self, response):
        html = response.read().decode()
        self.csrf = re.search('name="csrf" value="([^"]+)', html)[1]
        self.revision = re.search('name="revision" value="([^"]+)', html)[1]
        return html

    def post(self, url, fields, timeout=45):
        request = urllib.request.Request(
            url,
            data=urllib.parse.urlencode(fields).encode(),
            headers={"Origin": self.origin},
        )
        return self.opener.open(request, timeout=timeout)

    def save(self, base, allowed, token=""):
        form = {
            "enabled": "on",
            "days": "3",
            "url": base,
            "pat": token,
            "csrf": self.csrf,
            "revision": self.revision,
            **dict.fromkeys(allowed, "on"),
        }
        return self.refresh(self.post(self.url + "save", form))


class JiraAPI(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer " + TOKEN:
            self.send_error(401)
            return
        url = urllib.parse.urlsplit(self.path)
        params = {k: v[0] for k, v in urllib.parse.parse_qs(url.query).items()}
        try:
            if url.path == "/jira/rest/api/2/myself":
                data = {"name": "fixture"}
            elif url.path == "/jira/rest/api/2/search":
                data = {
                    "startAt": int(params["startAt"]),
                    "maxResults": int(params["maxResults"]),
                    "total": 3,
                    "issues": [
                        {"key": "TEST-7", "fields": {"summary": "Smoke issue"}}
                    ],
                }
            elif match := re.fullmatch(
                r"/jira/rest/api/2/issue/([A-Za-z0-9_]+-\d+|\d+)", url.path
            ):
                data = {
                    "key": match[1],
                    "fields": {
                        "summary": "Smoke issue",
                        "description": "test body",
                    },
                }
                if params["fields"] == "*all":
                    assert params["expand"] == "names,schema"
                    data.update(names={"customfield_1": "Поле"}, schema={})
                    data["fields"]["customfield_1"] = ["А", None]
            else:
                self.send_error(404)
                return
        except Exception as exc:
            self.server.failure = type(exc).__name__
            self.send_error(502)
            return
        self.server.calls += 1
        body = json.dumps(data, ensure_ascii=False).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def working_set(pid):
    from ctypes import wintypes

    class Counters(ctypes.Structure):
        _fields_ = [("cb", wintypes.DWORD), ("faults", wintypes.DWORD)] + [
            (key, ctypes.c_size_t)
            for key in ("peak", "current", "a", "b", "c", "d", "e", "f")
        ]

    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.OpenProcess.restype = wintypes.HANDLE
    handle = kernel.OpenProcess(0x410, False, pid)
    assert handle
    try:
        info = Counters()
        info.cb = ctypes.sizeof(info)
        ok = ctypes.windll.psapi.GetProcessMemoryInfo(
            wintypes.HANDLE(handle), ctypes.byref(info), info.cb
        )
        assert ok
        return info.current
    finally:
        kernel.CloseHandle(wintypes.HANDLE(handle))


def decode(result):
    assert not result.get("isError"), result["content"][0]["text"][:150]
    return json.loads(result["content"][0]["text"])


def smoke(exe, desktop=False):
    with (
        tempfile.TemporaryDirectory(prefix="engmcp-smoke-") as tmp,
        contextlib.ExitStack() as stack,
    ):
        root = pathlib.Path(tmp)
        env = dict(os.environ, ENGMCP_HOME=str(root / "profile"))
        upstream = ThreadingHTTPServer(("127.0.0.1", 0), JiraAPI)
        upstream.calls, upstream.failure = 0, None
        base = f"http://127.0.0.1:{upstream.server_port}/jira"
        threading.Thread(target=upstream.serve_forever, daemon=True).start()
        stack.callback(upstream.server_close)
        stack.callback(upstream.shutdown)

        def stop():
            subprocess.run([exe, "stop"], env=env, timeout=20, check=True)

        stack.callback(stop)
        with contextlib.closing(RPC([exe, "bridge"], env)) as first:
            first.init()
            assert first.tools() == set()
        Browser(exe, env).save(base, TOOLS, TOKEN)
        stop()
        started = time.perf_counter()
        a = RPC([exe, "bridge"], env)
        stack.callback(a.close)
        b = RPC([exe, "bridge"], env)
        stack.callback(b.close)
        a.init()
        b.init()
        assert a.tools() == b.tools() == TOOLS
        cold = (time.perf_counter() - started) * 1000
        assert upstream.calls == 0
        ui = Browser(exe, env)
        assert "Подключено" in ui.refresh(ui.post(ui.url + "check", {"csrf": ui.csrf}))
        assert a.tools() == b.tools() == TOOLS
        results = decode(
            a.tool(
                "jira_search_jql",
                {"jql": "updated IS NOT EMPTY ORDER BY updated DESC", "max_results": 1},
            )
        )
        assert results["issues"]
        key = results["issues"][0]["key"]
        word = re.search(r"\w+", results["issues"][0]["fields"]["summary"])
        decode(
            b.tool(
                "jira_search_text",
                {"text": word[0] if word else "test", "max_results": 1},
            )
        )
        with concurrent.futures.ThreadPoolExecutor(2) as pool:
            short = pool.submit(a.tool, "jira_get_issue", {"key": key})
            full = pool.submit(b.tool, "jira_get_all_fields", {"key": key})
            assert "description" in decode(short.result())["fields"]
            all_fields = decode(full.result())
            assert (
                all_fields["key"] == key
                and "names" in all_fields
                and "schema" in all_fields
            )
        assert all_fields["fields"]["customfield_1"] == ["А", None]
        assert (
            decode(
                b.tool(
                    "jira_search_jql",
                    {"jql": "filter = 10", "start_at": 2, "max_results": 1},
                )
            )["startAt"]
            == 2
        )
        if desktop:
            from desktop_clients import verify

            verify(exe, env, root, key)
        ui.save(base, TOOLS - {"jira_get_all_fields"})
        calls = upstream.calls
        for connection in [a, b]:
            assert "jira_get_all_fields" not in connection.tools()
            assert connection.tool("jira_get_all_fields", {"key": key})["isError"]
            assert connection.tool("jira_get_issue", {"key": key, "fields": "*all"})[
                "isError"
            ]
        assert upstream.calls == calls
        logs = pathlib.Path(env["ENGMCP_HOME"]) / "logs"
        audit = [
            json.loads(line)
            for path in logs.glob("audit-*.jsonl")
            for line in path.read_text().splitlines()
        ]
        assert any(e["result"] == "denied" for e in audit)
        settings_file = logs.parent / "settings.json"
        assert TOKEN.encode() not in settings_file.read_bytes()
        assert all(TOKEN.encode() not in p.read_bytes() for p in logs.iterdir())
        assert not upstream.failure
        print(
            f"Четыре операции и отзыв прав: OK. Все поля: {len(all_fields['fields'])}.",
            flush=True,
        )
        times = []
        for _ in range(100):
            t = time.perf_counter()
            decode(a.tool("jira_get_issue", {"key": key}))
            times.append(1000 * (time.perf_counter() - t))
        p95 = statistics.quantiles(times, n=100, method="inclusive")[94]
        starts = [
            event
            for path in logs.glob("diagnostic-*.jsonl")
            for line in path.read_text(encoding="utf-8").splitlines()
            if (event := json.loads(line))["result"] == "started"
        ]
        assert len(starts) == 2, (
            "Ожидался один сервер после одновременного запуска мостов"
        )
        mib = sum(
            working_set(pid) for pid in (starts[-1]["pid"], a.p.pid, b.p.pid)
        ) / (1024**2)
        print(
            f"До каталога {cold:.0f} мс; рабочий набор {mib:.1f} МиБ; p95 по 100 вызовам {p95:.2f} мс."
        )
        assert cold <= 2000 and mib <= 80 and p95 <= 50


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--exe", type=pathlib.Path, default=pathlib.Path("dist/engmcp.exe")
    )
    parser.add_argument("--desktop", action="store_true")
    args = parser.parse_args()
    smoke(args.exe.resolve(), args.desktop)
