"""Проверка локальных движков, поставляемых с настольными клиентами Windows."""

import contextlib
import json
import os
import pathlib
import subprocess
from smoke import RPC, TOOLS


def verify(exe, env, root, key):
    # Отдельный профиль и локальная сессия app-server. Запрос модели не нужен.
    desktop = os.environ.get("ENGMCP_CODEX_EXE")
    if not desktop:
        raise RuntimeError(
            "Укажи путь к codex.exe из Codex Desktop в ENGMCP_CODEX_EXE."
        )
    profile = root / "codex"
    profile.mkdir()
    config = (
        f'[mcp_servers.engmcp]\ncommand = {json.dumps(str(exe))}\nargs = ["bridge"]\n'
        f"[mcp_servers.engmcp.env]\nENGMCP_HOME = {json.dumps(env['ENGMCP_HOME'])}\n"
    )
    (profile / "config.toml").write_text(config, encoding="utf-8")
    isolated = {**env, "CODEX_HOME": str(profile)}
    with contextlib.closing(
        RPC([desktop, "app-server", "--listen", "stdio://"], isolated)
    ) as codex:
        codex.call(
            "initialize",
            {
                "clientInfo": {"name": "engmcp-check", "version": "1"},
                "capabilities": {"experimentalApi": True},
            },
        )
        codex.send({"method": "initialized"})
        thread = codex.call(
            "thread/start",
            {"cwd": str(root), "ephemeral": True, "approvalPolicy": "never"},
        )
        thread_id = thread["thread"]["id"]
        catalog = codex.call(
            "mcpServerStatus/list", {"threadId": thread_id, "serverName": "engmcp"}
        )
        tools = next(s["tools"] for s in catalog["data"] if s["name"] == "engmcp")
        assert set(tools) == TOOLS
        response = codex.call(
            "mcpServer/tool/call",
            {
                "server": "engmcp",
                "tool": "jira_get_issue",
                "threadId": thread_id,
                "arguments": {"key": key},
            },
        )
        assert (
            not response.get("isError")
            and json.loads(response["content"][0]["text"])["key"] == key
        )
    print("Codex Desktop app-server: каталог и чтение задачи OK.", flush=True)

    dsh = (
        pathlib.Path(env["LOCALAPPDATA"])
        / "Programs/DeepSeek Harness/DeepSeek Harness.exe"
    )
    source = pathlib.Path(__file__).with_name("dsh_probe.cjs")
    test = subprocess.run(
        [dsh, source, exe, key],
        env={**env, "ELECTRON_RUN_AS_NODE": "1"},
        capture_output=True,
        timeout=40,
    )
    if test.returncode or b"ENGMCP_DSH_OK" not in test.stdout:
        raise RuntimeError(
            "Harness: " + test.stderr.decode("utf-8", errors="replace")[-350:]
        )
    print(
        "DeepSeek Harness Desktop: штатная проверка и чтение комплектным MCP-клиентом OK.",
        flush=True,
    )
