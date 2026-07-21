# Client setup

All clients use the same remote Streamable HTTP MCP endpoint at `https://MEMORY_HOST/mcp` with `Authorization: Bearer ...`. `.mcp.json` configures Claude Code and `opencode.json` configures OpenCode from `MEMORY_URL` and `MEMORY_TOKEN`; each client will require its normal project trust/approval. The Codex example below uses its supported remote-MCP config.

The portable baseline is the short instruction in `AGENTS.md` and `CLAUDE.md`: call `catalog` before work and after context loss. This works even when hooks are unavailable.

Set `MEMORY_URL=https://MEMORY_HOST` and `MEMORY_TOKEN` in the agent's secret environment to enable the startup adapter. Set the non-secret `MEMORY_STARTUP_SPACE_ID` to the current project Space UUID when an agent can read many Spaces. Configure exactly one lifecycle hook per agent profile to invoke `bootstrap-catalog.sh` on start/resume/clear/compact. It prints one complete, host-approved routing envelope capped at 800 bytes; it never prints Memory cues or bodies. The script does not source repository environment files or send Git remotes. Codex requires the repository to be trusted and the hook definition to be reviewed; rejection only disables automatic bootstrap, not MCP.

The host must pass `ZESTMEM_STARTUP_CUE_APPROVALS` to `memoryd` as a JSON object from Space UUID to the SHA-256 of that Space's exact `use_when` value. Keep this value in root-owned host configuration, not agent or repository configuration. Changing a Space cue suppresses it at startup until the host approves the new hash. Agents can still call `catalog` and `recall` when the hook is unavailable.

For local testing in this checkout, `.codex/config.toml` and `.mcp.json` launch `memory-mcp-local.sh` with separate `codex` and `claude` profiles. The MCP wrapper reads revocable per-agent keys from the gitignored `.memory-client.env` and connects to the loopback service through `memory-mcp-bridge`. Never commit that file. The startup hook intentionally does not read it; inject startup credentials through the agent's secret environment. Replace the local wrapper configuration with the remote URL or Entra-backed bridge configuration when deploying elsewhere.

OpenCode should use the portable instruction until a stable, version-pinned lifecycle event is available in the deployed OpenCode version. Do not use experimental context transforms to capture conversations.

## Entra token refresh for unattended agents

Direct Streamable HTTP works with a fresh token. For long-running agent sessions, build `go build ./cmd/memory-mcp-bridge` and configure it as a local stdio MCP server. It forwards the remote server's live tool schemas and calls while Go's OAuth token source caches and reacquires short-lived Entra client-credentials tokens automatically.

Set `MEMORY_MCP_URL` and either `MEMORY_AUDIENCE` or `MEMORY_SCOPE`. The bridge uses Azure's `DefaultAzureCredential`, so it supports environment-based service-principal secrets or certificates, workload-identity federation, managed identity, and local Azure developer credentials without changing the MCP client. Configure the standard `AZURE_*` variables appropriate to the selected credential. `MEMORY_TOKEN` selects the simpler opaque-key mode during migration.

For direct Codex remote MCP configuration:

```toml
[mcp_servers.agent_memory]
url = "https://memory.example.com/mcp"
bearer_token_env_var = "MEMORY_TOKEN"
default_tools_approval_mode = "writes"
```

For automatic Entra renewal, configure Codex, Claude Code, or OpenCode to launch `memory-mcp-bridge` as a stdio MCP server with the environment above instead.
