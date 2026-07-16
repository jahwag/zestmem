# Agent guidance

When the agent-memory MCP server is configured, call `catalog` before task work and after context loss. Use `recall` only with task-specific queries, fetch full bodies only through `read_memory`, and write only when explicitly asked to preserve durable knowledge. Never store transcripts automatically.
