import {
  WorkflowDefinitionSchema,
  type WorkflowDefinition,
} from "./schemas";

export const WORKFLOW_BUILDER_INPUT_PREFIX =
  "MULTICA_WORKFLOW_BUILDER_INPUT_V1\n";

export const WORKFLOW_BUILDER_REPAIR_PREFIX =
  "MULTICA_WORKFLOW_BUILDER_FORMAT_REPAIR_V1\n";

export type WorkflowBuilderAgent = {
  id: string;
  name: string;
  description: string;
};

export type WorkflowBuilderDraft = {
  name: string;
  key: string;
  description: string;
  definition: WorkflowDefinition;
};

const WORKFLOW_KEY_RE = /^[a-z0-9][a-z0-9_-]*$/;

export function encodeWorkflowBuilderInput(
  request: string,
  agents: WorkflowBuilderAgent[],
): string {
  return (
    WORKFLOW_BUILDER_INPUT_PREFIX +
    JSON.stringify(
      {
        user_request: request,
        available_codex_agents: agents.map((agent) => ({
          id: agent.id,
          name: agent.name,
          description: agent.description,
        })),
      },
      null,
      2,
    )
  );
}

export function encodeWorkflowBuilderRepairInput(
  request: string,
  agents: WorkflowBuilderAgent[],
): string {
  return (
    WORKFLOW_BUILDER_REPAIR_PREFIX +
    "Your previous response could not be read as a workflow draft. Return the complete draft again. " +
    "End with exactly one <workflow_draft>{valid compact JSON}</workflow_draft> block. " +
    "Do not use Markdown fences and do not omit the block.\n" +
    JSON.stringify({
      user_request: request,
      available_codex_agents: agents.map((agent) => ({
        id: agent.id,
        name: agent.name,
        description: agent.description,
      })),
    })
  );
}

export function parseWorkflowBuilderDraft(
  content: string,
): WorkflowBuilderDraft | null {
  const candidates: string[] = [];
  const tagged = content.match(
    /<workflow_draft>([\s\S]*?)<\/workflow_draft>/i,
  );
  if (tagged?.[1]) candidates.push(tagged[1]);

  for (const match of content.matchAll(/```(?:json)?\s*([\s\S]*?)```/gi)) {
    if (match[1]) candidates.push(match[1]);
  }
  candidates.push(content);

  for (const candidate of candidates) {
    const direct = parseDraftJson(candidate.trim());
    const parsed = normalizeWorkflowBuilderDraft(direct);
    if (parsed) return parsed;

    for (const object of extractJsonObjects(candidate)) {
      const nested = normalizeWorkflowBuilderDraft(parseDraftJson(object));
      if (nested) return nested;
    }
  }
  return null;
}

function normalizeWorkflowBuilderDraft(raw: unknown): WorkflowBuilderDraft | null {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
  let record = raw as Record<string, unknown>;
  if (
    record.workflow_draft &&
    typeof record.workflow_draft === "object" &&
    !Array.isArray(record.workflow_draft)
  ) {
    record = record.workflow_draft as Record<string, unknown>;
  }
  const name = typeof record.name === "string" ? record.name.trim() : "";
  const key =
    typeof record.key === "string" ? record.key.trim().toLowerCase() : "";
  const description =
    typeof record.description === "string" ? record.description.trim() : "";
  if (
    !name ||
    name.length > 200 ||
    !key ||
    key.length > 128 ||
    !WORKFLOW_KEY_RE.test(key)
  ) {
    return null;
  }

  const definition = WorkflowDefinitionSchema.safeParse(
    normalizeWorkflowDefinitionInput(record.definition),
  );
  if (!definition.success) return null;
  return { name, key, description, definition: definition.data };
}

function normalizeWorkflowDefinitionInput(value: unknown): unknown {
  if (!value || typeof value !== "object" || Array.isArray(value)) return value;
  const definition = value as Record<string, unknown>;
  if (!Array.isArray(definition.nodes)) return value;

  return {
    ...definition,
    nodes: definition.nodes.map((node) => {
      if (!node || typeof node !== "object" || Array.isArray(node)) return node;
      const source = node as Record<string, unknown>;
      const normalized: Record<string, unknown> = { ...source };

      if (typeof normalized.key !== "string" && typeof source.id === "string") {
        normalized.key = source.id;
      }
      if (typeof source.next === "string") {
        normalized.next = source.next.trim() ? [source.next.trim()] : [];
      }
      if (
        !source.routing &&
        typeof source.agent_id === "string" &&
        source.agent_id.trim()
      ) {
        normalized.routing = {
          strategy: "explicit",
          agent_id: source.agent_id.trim(),
        };
      }
      if (
        !Array.isArray(source.rework_targets) &&
        typeof source.rework_target === "string"
      ) {
        normalized.rework_targets = source.rework_target.trim()
          ? [source.rework_target.trim()]
          : [];
      }
      if (typeof source.acceptance_criteria === "string") {
        normalized.acceptance_criteria = source.acceptance_criteria.trim()
          ? [source.acceptance_criteria.trim()]
          : [];
      }

      delete normalized.id;
      delete normalized.agent_id;
      delete normalized.rework_target;
      return normalized;
    }),
  };
}

function extractJsonObjects(value: string): string[] {
  const objects: string[] = [];
  let start = -1;
  let depth = 0;
  let inString = false;
  let escaped = false;

  for (let index = 0; index < value.length; index += 1) {
    const character = value[index];
    if (start < 0) {
      if (character === "{") {
        start = index;
        depth = 1;
      }
      continue;
    }
    if (inString) {
      if (escaped) escaped = false;
      else if (character === "\\") escaped = true;
      else if (character === '"') inString = false;
      continue;
    }
    if (character === '"') inString = true;
    else if (character === "{") depth += 1;
    else if (character === "}") {
      depth -= 1;
      if (depth === 0) {
        objects.push(value.slice(start, index + 1));
        start = -1;
      }
    }
  }
  return objects;
}

function parseDraftJson(value: string): unknown {
  try {
    return JSON.parse(value);
  } catch {
    try {
      return JSON.parse(escapeJsonStringControlCharacters(value));
    } catch {
      return null;
    }
  }
}

function escapeJsonStringControlCharacters(value: string): string {
  let result = "";
  let inString = false;
  let escaped = false;
  for (const character of value) {
    if (!inString) {
      result += character;
      if (character === '"') inString = true;
      continue;
    }
    if (escaped) {
      result += character;
      escaped = false;
    } else if (character === "\\") {
      result += character;
      escaped = true;
    } else if (character === '"') {
      result += character;
      inString = false;
    } else if (character === "\n") {
      result += "\\n";
    } else if (character === "\r") {
      result += "\\r";
    } else if (character === "\t") {
      result += "\\t";
    } else {
      result += character;
    }
  }
  return result;
}
