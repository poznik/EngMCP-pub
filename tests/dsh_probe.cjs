// Штатная проверка подключения Harness и чтение задачи его комплектным MCP-клиентом.
const path = require("node:path");
const assert = require("node:assert/strict");
const { createRequire } = require("node:module");

async function main() {
  const [command, key] = process.argv.slice(2);
  const entry = path.join(
    path.dirname(process.execPath),
    "resources",
    "app.asar",
    "dsh",
    "node_modules",
    "@deepseek-ai",
    "dsh-mcp-client",
    "lib",
    "index.js",
  );
  const plugin = require(entry);
  const bundled = createRequire(entry);
  const { Client } = bundled("@modelcontextprotocol/client");
  const { StdioClientTransport } = bundled(
    "@modelcontextprotocol/client/stdio",
  );
  const config = {
    serverName: "engmcp",
    transport: "stdio",
    command,
    args: ["bridge"],
    env: { ENGMCP_HOME: process.env.ENGMCP_HOME },
    closeTimeoutMs: 3000,
  };
  const probe = await plugin.probeServer({}, config);
  assert.equal(probe.ok, true, "Проба подключения Harness завершилась ошибкой");
  assert.equal(probe.serverInfo.name, "engmcp");
  assert.equal(probe.toolNames.length, 4);
  const client = new Client(
    { name: "engmcp-dsh-check", version: "1" },
    { capabilities: {}, versionNegotiation: { mode: "auto" } },
  );
  try {
    await client.connect(new StdioClientTransport(config));
    const result = await client.callTool({
      name: "jira_get_issue",
      arguments: { key },
    });
    assert.ok(!result.isError);
    assert.equal(JSON.parse(result.content[0].text).key, key);
  } finally {
    await client.close();
  }
  console.log("ENGMCP_DSH_OK");
}
main().catch((error) => {
  console.error(error.message);
  process.exitCode = 1;
});
