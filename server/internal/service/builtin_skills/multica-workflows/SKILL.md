---
name: multica-workflows
description: "Use when listing, validating, starting, inspecting, cancelling, or deciding acceptance for a Multica workflow."
user-invocable: false
allowed-tools: Bash(multica workflow *)
---

# Multica Workflows

The PostgreSQL Workflow Engine is authoritative. CLI and MCP are transport adapters for Workflow Action Contract v1; do not implement orchestration or state transitions outside the server engine.

Use the versioned, closed action inputs:

```bash
multica workflow call template.list --input-json '{"schema_version":"1"}' --output json
multica workflow call template.get --input-json '{"schema_version":"1","template_id":"<id>"}' --output json
multica workflow call template.validate --input-json '{"schema_version":"1","definition":{...}}' --output json
multica workflow call run.start --input-json '{"schema_version":"1","template_id":"<id>","idempotency_key":"<stable-key>","title":"...","description":"...","input":{}}' --output json
multica workflow call run.get --input-json '{"schema_version":"1","run_id":"<id>"}' --output json
multica workflow call run.cancel --input-json '{"schema_version":"1","run_id":"<id>"}' --output json
multica workflow call run.decide_acceptance --input-json '{"schema_version":"1","run_id":"<id>","accept":true}' --output json
```

Persist and reuse `idempotency_key` for retries. Reusing it with changed workflow input returns `idempotency_conflict`. `run.start`, `run.cancel`, and `run.decide_acceptance` mutate durable state; invoke them only when the task requires that side effect. Never place credentials in action JSON, comments, or logs.

To expose the same seven actions over MCP stdio, run `multica workflow mcp serve`. MCP `tools/list` is the source of truth for exact JSON Schemas.
