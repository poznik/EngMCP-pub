"""Приёмка установщика в чистом профиле текущего пользователя и панели в Chromium."""

import contextlib
import os
import pathlib
import subprocess
import tempfile
import threading
import winreg

from playwright.sync_api import sync_playwright
from smoke import RPC, TOKEN, TOOLS, JiraAPI, ThreadingHTTPServer


def check(setup):
    # Проверка использует HKCU-запись Inno Setup. Существующую установку сохраняем.
    key = r"Software\Microsoft\Windows\CurrentVersion\Uninstall\EngMCP_is1"
    try:
        with winreg.OpenKey(winreg.HKEY_CURRENT_USER, key):
            raise RuntimeError(
                "На ПК уже установлен EngMCP. Запусти проверку в чистом профиле."
            )
    except FileNotFoundError:
        pass
    with (
        tempfile.TemporaryDirectory(prefix="engmcp-setup-") as tmp,
        contextlib.ExitStack() as stack,
    ):
        root = pathlib.Path(tmp)
        app_dir = root / "Program Files" / "EngMCP"
        env = dict(os.environ, ENGMCP_HOME=str(root / "state"))
        flags = [
            "/VERYSILENT",
            "/NORESTART",
            "/SUPPRESSMSGBOXES",
            "/NOICONS",
            "/TASKS=",
        ]

        def install():
            subprocess.run(
                [setup, *flags, f"/DIR={app_dir}"], check=True, env=env, timeout=60
            )

        install()
        exe = app_dir / "engmcp.exe"
        assert subprocess.check_output([exe, "version"], env=env).strip() == b"0.3.0"
        uninstaller = next(app_dir.glob("unins*.exe"))

        def uninstall():
            subprocess.run([uninstaller, *flags], check=True, env=env, timeout=60)
            assert not exe.exists()

        stack.callback(uninstall)
        server = ThreadingHTTPServer(("127.0.0.1", 0), JiraAPI)
        server.calls = 0
        threading.Thread(target=server.serve_forever, daemon=True).start()
        stack.callback(server.server_close)
        stack.callback(server.shutdown)
        with sync_playwright() as pw:
            browser = pw.chromium.launch(channel="chrome", headless=True)
            page = browser.new_page(viewport={"width": 1000, "height": 920})

            def login():
                url = (
                    subprocess.check_output([exe, "panel-url"], env=env, timeout=12)
                    .decode()
                    .strip()
                )
                page.goto(url)
                page.get_by_role("heading", name="Jira Data Center").wait_for()

            login()
            page.get_by_label("Включить модуль").check()
            page.get_by_label("Адрес Jira").fill(
                f"http://127.0.0.1:{server.server_port}/jira"
            )
            page.get_by_label("Персональный токен").fill(TOKEN)
            page.get_by_label("Срок хранения").fill("3")
            page.get_by_role("button", name="Сохранить настройки").press("Enter")
            page.get_by_text("Настройки сохранены.").wait_for()
            assert page.get_by_label("Персональный токен").input_value() == ""
            page.get_by_role("button", name="Проверить сохранённое подключение").click()
            page.get_by_text("Подключено. Учётная запись — fixture").wait_for()
            page.get_by_label("Все поля задачи").uncheck()
            page.get_by_role("button", name="Сохранить настройки").click()
            page.get_by_text("Настройки сохранены.").wait_for()
            page.set_viewport_size({"width": 390, "height": 840})
            assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
            install()
            login()
            assert not page.get_by_label("Все поля задачи").is_checked()
            assert page.get_by_label("Срок хранения").input_value() == "3"
            page.get_by_role("button", name="Проверить сохранённое подключение").click()
            page.get_by_text("Подключено. Учётная запись — fixture").wait_for()
            browser.close()
        with contextlib.closing(RPC([exe, "bridge"], env)) as client:
            client.init()
            assert client.tools() == TOOLS - {"jira_get_all_fields"}
        assert TOKEN.encode() not in (root / "state/settings.json").read_bytes()
    print(
        "Установка, панель Chromium, обновление с сохранением PAT и прав, удаление: OK."
    )


if __name__ == "__main__":
    check(pathlib.Path("dist/EngMCP-Setup-x64.exe").resolve())
