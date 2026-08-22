# 07 — MCP Proxy

MCP is first-class; users may register any MCP server.

- **Connection management**: users register MCP servers (streamable HTTP preferred; stdio servers run in platform-managed sidecar sandboxes). OAuth-based MCP auth stored per-user.
- **Per-user routing**: proxy maps `(run_token.user, server_ref)` → that user's connection and credentials. Two users on the same MCP server never share sessions or caches.
- **Tool filtering**: `allowedTools` in the agent spec intersects with the user's granted scopes — the model only ever sees the resulting tool list.
- **Namespacing**: MCP tools exposed to the agent as `mcp__<server>__<tool>` to avoid collisions.
- Protocol handling: JSON-RPC multiplexing, notification fan-out, timeout/retry policy, schema validation of responses before they enter model context.
