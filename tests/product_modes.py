"""Приёмка двух вкладок и независимых режимов в Chromium и настоящей stdio-сессии."""

import argparse
import contextlib
import json
import os
import pathlib
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

from playwright.sync_api import expect, sync_playwright
from smoke import RPC, TOOLS


class API(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        path = urlsplit(self.path).path
        if path.startswith("/wiki/"):
            token = "fixture-wiki"
            payload = {
                "id": "42",
                "displayName": "Wiki Fixture",
                "body": {"storage": {"value": "fixture body"}},
            }
            if path == "/wiki/rest/api/content/search":
                payload = {"results": [{"id": "42"}], "size": 1}
        elif path.startswith("/jira/"):
            token = "fixture-jira"
            payload = {"name": "Jira Fixture"}
        else:
            self.send_error(404)
            return
        if self.headers.get("Authorization") != "Bearer " + token:
            self.send_error(401)
            return
        self.server.calls += 1
        data = json.dumps(payload).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def check_copy_buttons(page):
    page.context.grant_permissions(["clipboard-read", "clipboard-write"])
    for name in ["Codex Desktop", "DeepSeek Harness Desktop", "Claude Desktop"]:
        summary = page.get_by_text(name, exact=True)
        details = page.locator("details").filter(has=summary)
        summary.click()
        control = details.get_by_role("button", name="Копировать", exact=True)
        expect(control).to_have_css("opacity", "0")
        details.locator("pre").hover()
        expect(control).to_have_css("opacity", "1")
        summary.hover()
        expect(control).to_have_css("opacity", "0")
        summary.press("Tab")
        expect(control).to_be_focused()
        expect(control).to_have_css("opacity", "1")
        control.press("Enter")
        expect(details.get_by_role("status")).to_have_text("Скопировано")
        expect(control.locator(".check-icon")).to_be_visible()
        copied = page.evaluate("navigator.clipboard.readText()")
        assert copied.replace("\r\n", "\n") == details.locator("pre").text_content(), name
        summary.click()


def run(exe):
    with (
        tempfile.TemporaryDirectory(prefix="engmcp-modes-") as tmp,
        contextlib.ExitStack() as stack,
    ):
        env = dict(os.environ, ENGMCP_HOME=tmp)
        server = ThreadingHTTPServer(("127.0.0.1", 0), API)
        server.calls = 0
        threading.Thread(target=server.serve_forever, daemon=True).start()
        stack.callback(server.server_close)
        stack.callback(server.shutdown)
        stack.callback(subprocess.run, [exe, "stop"], env=env, check=True, timeout=20)
        url = subprocess.check_output([exe, "panel-url"], env=env).decode().strip()
        with sync_playwright() as pw:
            browser = pw.chromium.launch(channel="chrome", headless=True)
            page = browser.new_page()
            page.goto(url)
            check_copy_buttons(page)
            claude = page.locator("details").filter(
                has=page.get_by_text("Claude Desktop", exact=True)
            )
            claude.locator("summary").click()
            config = json.loads(claude.locator("pre").inner_text())["mcpServers"]["engmcp"]
            assert pathlib.Path(config["command"]) == exe and config["args"] == ["bridge"]
            client = RPC([config["command"], *config["args"]], env)
            stack.callback(client.close)
            client.init()
            for service, path in [("Jira", "jira"), ("Confluence", "wiki")]:
                page.get_by_role("link", name=service, exact=True).click()
                page.get_by_label("Адрес " + service, exact=True).fill(
                    f"http://127.0.0.1:{server.server_port}/{path}"
                )
                page.get_by_label("Включить модуль").check()
                pat = page.get_by_label("Персональный токен")
                assert pat.get_attribute("placeholder") == "Введи токен сервиса"
                pat.fill("fixture-" + path)
                page.get_by_role("button", name="Сохранить настройки").click()
                assert pat.get_attribute("placeholder") == "********"
                assert pat.input_value() == ""
                assert page.get_by_role("checkbox", name="Удалить").count() == 0
                page.get_by_role(
                    "button", name="Проверить сохранённое подключение"
                ).click()
                page.get_by_text("Подключено.", exact=False).wait_for()
            wiki_tools = {"confluence_search_cql", "confluence_get_page"}
            assert client.tools() == TOOLS | wiki_tools
            for tool, args in [
                ("confluence_get_page", {"id": "42"}),
                ("confluence_search_cql", {"cql": "type=page"}),
            ]:
                assert not client.tool(tool, args).get("isError")
            page.get_by_label("Содержимое страницы").uncheck()
            page.get_by_role("button", name="Сохранить настройки").click()
            assert client.tools() == TOOLS | {"confluence_search_cql"}
            count = server.calls
            assert (
                client.tool("confluence_get_page", {"id": "42"})["isError"]
                and server.calls == count
            )
            helper = exe.parent / "engmcp-gateway" / "engmcp-gateway.exe"
            if helper.is_file():
                page.get_by_role("link", name="Шлюз 2FA").click()
                password = page.get_by_label("Пароль шлюза")
                seed = page.get_by_label("TOTP-секрет")
                for field in (password, seed):
                    assert not field.get_attribute("placeholder")
                page.get_by_label("HTTPS-адрес шлюза").fill("https://mfa.example.org")
                page.get_by_label("Логин шлюза").fill("fixture")
                password.fill("fixture-password")
                seed.fill("JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP")
                page.get_by_role("button", name="Сохранить настройки").click()
                page.get_by_text("Настройки сохранены.").wait_for()
                for field in (password, seed):
                    assert field.get_attribute("placeholder") == "********"
                    assert field.input_value() == ""
                assert page.get_by_role("checkbox", name="Удалить").count() == 0
                revision = page.locator("input[name=revision]").input_value()
                page.get_by_role("button", name="Сохранить настройки").click()
                page.get_by_text("Настройки сохранены.").wait_for()
                assert page.locator("input[name=revision]").input_value() == revision
                page.get_by_role("link", name="Confluence", exact=True).click()
                page.get_by_label("Адрес Confluence").fill("https://wiki.example.org")
                page.get_by_label("Персональный токен").fill("fixture-wiki")
                page.get_by_label("Режим подключения").select_option("gateway")
                page.get_by_role("button", name="Сохранить настройки").click()
                page.get_by_text("Настройки сохранены.").wait_for()
                page.reload()
                assert page.get_by_label("Режим подключения").input_value() == "gateway"
                page.get_by_role("link", name="Jira", exact=True).click()
                assert page.get_by_label("Режим подключения").input_value() == "direct"
            else:
                expect(page.get_by_role("navigation").get_by_role("link")).to_have_count(5)
                expect(page.get_by_role("link", name="Шлюз 2FA")).to_have_count(0)
                expect(page.get_by_label("Режим подключения")).to_have_count(0)
                page.get_by_role("link", name="Jira", exact=True).click()
                expect(page.get_by_label("Режим подключения")).to_have_count(0)
            page.get_by_role("button", name="Проверить сохранённое подключение").click()
            page.get_by_text("Подключено. Учётная запись — Jira Fixture").wait_for()
            browser.close()
        for f in [
            pathlib.Path(tmp) / "settings.json",
            *(pathlib.Path(tmp) / "logs").glob("*.jsonl"),
        ]:
            data = f.read_bytes()
            for s in [
                "fixture-password",
                "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP",
                "fixture-wiki",
                "fixture-jira",
            ]:
                assert s.encode() not in data
    print(
        "Windows: Jira и Confluence, сохранение профиля, независимые режимы и права — OK."
    )


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--exe", type=pathlib.Path, default=pathlib.Path("dist/engmcp.exe")
    )
    run(parser.parse_args().exe.resolve())
