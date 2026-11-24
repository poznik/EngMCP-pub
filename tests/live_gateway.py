"""Проверка установленного модуля с явно указанным профилем. Содержимое сервиса остаётся в памяти."""

import argparse
import contextlib
import html
import os
import pathlib
import re
import subprocess
import tempfile
import urllib.error

from dotenv import dotenv_values
from smoke import Browser


def check(exe, profile, service, base, gateway):
    cfg = dotenv_values(profile)

    def value(*keys):
        for key in keys:
            if cfg.get(key):
                return cfg[key]
        raise ValueError("В выбранном профиле отсутствует " + keys[0])

    gateway = gateway or value("NETSCALER_BASE")
    base = base or value("JIRA_BASE" if service == "jira" else "CONFLUENCE_BASE")
    token = value("JIRA_PAT" if service == "jira" else "CONFLUENCE_PAT")
    with (
        tempfile.TemporaryDirectory(prefix="engmcp-live-") as root,
        contextlib.ExitStack() as stack,
    ):
        env = dict(os.environ, ENGMCP_HOME=root)
        stack.callback(subprocess.run, [exe, "stop"], env=env, check=True, timeout=20)
        ui = Browser(exe, env)

        def save(fields):
            form = {"csrf": ui.csrf, "revision": ui.revision, "days": "1", **fields}
            ui.refresh(ui.post(ui.url + "save", form))

        save(
            {
                "service": "gateway",
                "gateway_url": gateway,
                "user": value("MFA_USER", "JIRA_USER"),
                "password": value("MFA_PASS", "JIRA_PASS"),
                "seed": value("MFA_TOTP_SECRET", "JIRA_TOTP_SECRET"),
            }
        )
        save(
            {
                "service": service,
                "mode": "gateway",
                "url": base,
                "pat": token,
                "enabled": "on",
            }
        )
        try:
            page = ui.refresh(
                ui.post(
                    ui.url + "check", {"csrf": ui.csrf, "service": service}, timeout=110
                )
            )
        except urllib.error.HTTPError as error:
            body = error.read().decode()
            message = re.search(r'<p class="error" role="alert">([^<]+)</p>', body)
            raise SystemExit(
                html.unescape(message[1]) if message else f"HTTP {error.code}"
            ) from None
        if "Подключено. Учётная запись" not in page:
            raise RuntimeError("Учётная запись не подтверждена.")
        print(f"{service}: учётная запись подтверждена через NetScaler.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--exe", type=pathlib.Path, default=pathlib.Path("dist/engmcp.exe")
    )
    parser.add_argument("--env", type=pathlib.Path, required=True)
    parser.add_argument("--service", choices=["jira", "confluence"], required=True)
    parser.add_argument("--base")
    parser.add_argument("--gateway")
    args = parser.parse_args()
    check(args.exe.resolve(), args.env, args.service, args.base, args.gateway)
