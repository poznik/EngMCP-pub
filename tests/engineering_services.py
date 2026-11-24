"""Контракт GitLab, Jenkins и Allure через собранный EngMCP, веб-панель и MCP stdio."""

import argparse
import base64
import contextlib
import json
import os
import pathlib
import subprocess
import tempfile
import threading
import urllib.error
from email.parser import BytesParser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

from smoke import Browser, RPC

BUILD = "number,url,result,building,timestamp,duration,displayName"
JENKINS_AUTH = "Basic " + base64.b64encode(b"builder:fixture-jenkins").decode()
# Имя MCP, аргументы, ожидаемые путь и query. Ответы содержат только вымышленные данные.
CASES = {
    "gitlab_list_projects": (
        {"search": "tools & sdk", "page": 2},
        "/gitlab/api/v4/projects",
        {"search": "tools & sdk", "page": "2", "per_page": "25"},
        [{"id": 42}],
    ),
    "gitlab_list_merge_requests": (
        {"project_id": 42},
        "/gitlab/api/v4/projects/42/merge_requests",
        {"page": "1", "per_page": "25"},
        [{"iid": 7}],
    ),
    "gitlab_get_merge_request": (
        {"project_id": 42, "iid": 7},
        "/gitlab/api/v4/projects/42/merge_requests/7",
        {},
        {"iid": 7, "title": "Fixture MR"},
    ),
    "gitlab_list_pipelines": (
        {"project_id": 42, "per_page": 3},
        "/gitlab/api/v4/projects/42/pipelines",
        {"page": "1", "per_page": "3"},
        [{"id": 15, "status": "success"}],
    ),
    "jenkins_list_jobs": (
        {"folder": "QA Team", "start": 2, "limit": 4},
        "/jenkins/job/QA%20Team/api/json",
        {"tree": "jobs[name,fullName,url,color]{2,6}"},
        {"jobs": [{"name": "tests"}]},
    ),
    "jenkins_list_builds": (
        {"job": "QA Team/tests"},
        "/jenkins/job/QA%20Team/job/tests/api/json",
        {"tree": "builds[" + BUILD + "]{0,25}"},
        {"builds": [{"number": 9}]},
    ),
    "jenkins_get_build": (
        {"job": "QA Team/tests", "number": 9},
        "/jenkins/job/QA%20Team/job/tests/9/api/json",
        {"tree": BUILD},
        {"number": 9, "result": "SUCCESS"},
    ),
    "allure_list_launches": (
        {"project_id": 2, "page": 1, "size": 4},
        "/allure/api/rs/launch",
        {"projectId": "2", "page": "1", "size": "4"},
        {"content": [{"id": 5}], "totalElements": 5},
    ),
    "allure_get_launch": (
        {"id": 5},
        "/allure/api/rs/launch/5",
        {},
        {"id": 5, "name": "Fixture launch"},
    ),
    "allure_get_test_case": (
        {"id": 19},
        "/allure/api/rs/testcase/19",
        {},
        {"id": 19, "name": "Fixture case"},
    ),
}
IDENTITIES = {
    "/gitlab/api/v4/user": {"username": "builder"},
    "/jenkins/whoAmI/api/json": {"name": "builder", "authenticated": True},
    "/allure/api/uaa/me": {"username": "builder"},
}


class ServiceAPI(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, status, data):
        payload = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_POST(self):
        if self.path != "/allure/api/uaa/oauth/token" or self.headers.get(
            "Authorization"
        ):
            return self.reply(400, {})
        body = self.rfile.read(int(self.headers["Content-Length"]))
        message = BytesParser().parsebytes(
            b"Content-Type: "
            + self.headers["Content-Type"].encode()
            + b"\r\n\r\n"
            + body
        )
        fields = {
            part.get_param("name", header="Content-Disposition"): part.get_payload()
            for part in message.get_payload()
        }
        if fields != {
            "grant_type": "apitoken",
            "scope": "openid",
            "token": "fixture-allure",
        }:
            return self.reply(401, {})
        self.server.exchanges += 1
        self.server.bearer = "fixture-access-" + str(self.server.exchanges)
        return self.reply(
            200,
            {
                "access_token": self.server.bearer,
                "expires_in": 600,
                "token_type": "bearer",
            },
        )

    def do_GET(self):
        self.server.reads += 1
        target = urlsplit(self.path)
        headers = self.headers
        if target.path.startswith("/gitlab/"):
            authorized = headers.get(
                "PRIVATE-TOKEN"
            ) == "fixture-gitlab" and not headers.get("Authorization")
        elif target.path.startswith("/jenkins/"):
            authorized = headers.get(
                "Authorization"
            ) == JENKINS_AUTH and not headers.get("PRIVATE-TOKEN")
        else:
            authorized = (
                bool(self.server.bearer)
                and headers.get("Authorization") == "Bearer " + self.server.bearer
                and not headers.get("PRIVATE-TOKEN")
            )
        if not authorized:
            return self.reply(401, {})
        if target.path == "/jenkins/whoAmI/api/json" and self.server.anonymous:
            return self.reply(
                200, {"name": "anonymous", "authenticated": True, "anonymous": True}
            )
        if target.path in IDENTITIES:
            return self.reply(200, IDENTITIES[target.path])
        query = {k: v[0] for k, v in parse_qs(target.query).items()}
        for _, path, params, result in CASES.values():
            if target.path == path and query == params:
                return self.reply(200, result)
        return self.reply(400, {})


def run(exe):
    with (
        tempfile.TemporaryDirectory(prefix="engmcp-services-") as root,
        contextlib.ExitStack() as stack,
    ):
        cfg = pathlib.Path(root) / "settings.json"
        legacy = {
            "version": 2,
            "retention_days": 37,
            "products": {
                id: {"enabled": False, "mode": "direct", "allowed": {}}
                for id in ("jira", "confluence")
            },
        }
        before = json.dumps(legacy).encode()
        cfg.write_bytes(before)
        env = dict(os.environ, ENGMCP_HOME=root)
        api = ThreadingHTTPServer(("127.0.0.1", 0), ServiceAPI)
        api.exchanges = api.reads = 0
        api.bearer = ""
        api.anonymous = False
        threading.Thread(target=api.serve_forever, daemon=True).start()
        stack.callback(api.server_close)
        stack.callback(api.shutdown)
        stack.callback(subprocess.run, [exe, "stop"], env=env, check=True, timeout=20)
        client = RPC([exe, "bridge"], env)
        stack.callback(client.close)
        client.init()
        ui = Browser(exe, env)
        assert not client.tools() and cfg.read_bytes() == before

        def save(product, allowed, **extra):
            fields = {
                "csrf": ui.csrf,
                "revision": ui.revision,
                "service": product,
                "days": "37",
                "enabled": "on",
                "url": f"http://127.0.0.1:{api.server_port}/{product}",
                "username": "builder",
                **dict.fromkeys(allowed, "on"),
                **extra,
            }
            return ui.refresh(ui.post(ui.url + "save", fields))

        def tools_for(product):
            return {k for k in CASES if k.startswith(product + "_")}

        for product in ("gitlab", "jenkins", "allure"):
            page = ui.refresh(ui.opener.open(ui.url + "?service=" + product))
            assert 'value="' + product + '"' in page and 'name="mode"' not in page
            assert ('name="username"' in page) == (product == "jenkins")
            assert 'placeholder="Введи токен сервиса"' in page
            page = save(product, tools_for(product), pat="fixture-" + product)
            assert 'placeholder="********"' in page and 'name="forget"' not in page
            page = ui.refresh(
                ui.post(ui.url + "check", {"csrf": ui.csrf, "service": product})
            )
            assert "Подключено." in page and "builder" in page
        api.anonymous = True
        try:
            ui.post(ui.url + "check", {"csrf": ui.csrf, "service": "jenkins"})
        except urllib.error.HTTPError as error:
            assert error.code == 502
        else:
            raise AssertionError("Анонимная сессия Jenkins принята за авторизованную")
        api.anonymous = False
        assert client.tools() == set(CASES)
        for name, (args, _, _, response) in CASES.items():
            reply = client.tool(name, args)
            assert (
                not reply.get("isError")
                and json.loads(reply["content"][0]["text"]) == response
            ), name
        assert api.exchanges == 1
        api.bearer = ""  # Сервис досрочно завершил сессию.
        assert not client.tool("allure_get_launch", {"id": 5}).get("isError")
        assert api.exchanges == 2
        save("gitlab", tools_for("gitlab") - {"gitlab_get_merge_request"})
        count = api.reads
        assert client.tool("gitlab_get_merge_request", {"project_id": 42, "iid": 7})[
            "isError"
        ]
        assert client.tool("jenkins_get_build", {"job": "..", "number": 9})["isError"]
        assert client.tool("allure_list_launches", {"project_id": 2, "size": 101})[
            "isError"
        ]
        assert api.reads == count
        assert client.tools() == set(CASES) - {"gitlab_get_merge_request"}
        for service, change in (
            ("jenkins", {"username": "other"}),
            ("gitlab", {"mode": "gateway"}),
        ):
            try:
                save(service, tools_for(service), **change)
            except urllib.error.HTTPError as error:
                assert error.code == 400
            else:
                raise AssertionError("Панель приняла несовместимую конфигурацию")
        for path in [cfg, *pathlib.Path(root, "logs").glob("*.jsonl")]:
            data = path.read_bytes()
            for secret in (
                b"fixture-access-",
                b"fixture-jenkins",
                b"fixture-gitlab",
                b"fixture-allure",
                JENKINS_AUTH.encode(),
            ):
                assert secret not in data
        saved = json.loads(cfg.read_bytes())
        assert (
            saved["retention_days"] == 37 and not saved["products"]["jira"]["allowed"]
        )
    print(
        "Три подключения через панель, 10 операций MCP, отзыв прав, обмен токена и миграция 0.2.0 — OK."
    )


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--exe", type=pathlib.Path, default=pathlib.Path("dist/engmcp.exe")
    )
    run(parser.parse_args().exe.resolve())
