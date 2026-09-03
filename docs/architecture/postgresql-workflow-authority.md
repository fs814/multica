# PostgreSQL Workflow Engine Authority

Status: accepted

Multica's PostgreSQL-backed Workflow Engine is the only orchestration authority. Every entry point—UI, HTTP intake, CLI, MCP, webhook, and autopilot—must invoke the same engine commands and persist state in `workflow_run`, `workflow_step_instance`, and `workflow_event`. Adapters may validate and translate transport data, but they must not implement a second state machine.

Workflow Action Contract v1 is the shared automation surface. It exposes template list/get/validate and run start/get/cancel/acceptance-decision actions. Inputs are closed, versioned JSON Schemas; unknown fields and unknown schema versions fail closed. Automated run starts require a caller-stable idempotency key. Authentication remains in the existing API client and credentials must never enter action results, MCP tool output, events, or logs.

`workflow_event.id` is the durable event identity. Callback delivery and realtime publication carry the same versioned CloudEvents-aligned envelope after the database transaction commits. Callback retries retain that identity and the existing durable callback lease/replay machinery.

Temporal is not part of this implementation or deployment. Evaluating it would require a separate ADR covering migration, cutover, rollback, consistency, operational ownership, and the retirement of the PostgreSQL engine; it must not be introduced as a concurrent authority.

The JSON canvas remains restricted to the seven supported node types. Subflows, an expression language, a plugin marketplace, and full BPMN are explicitly outside this decision.
