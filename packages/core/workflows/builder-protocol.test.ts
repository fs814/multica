import { describe, expect, it } from "vitest";
import {
  encodeWorkflowBuilderInput,
  encodeWorkflowBuilderRepairInput,
  parseWorkflowBuilderDraft,
} from "./builder-protocol";

const validDefinition = {
  schema_version: 1,
  entry_node: "input",
  nodes: [
    { key: "input", type: "input", next: ["work"], input_fields: [] },
    {
      key: "work",
      type: "agent",
      instruction: "Do the work",
      next: ["end"],
      routing: { strategy: "explicit", agent_id: "agent-1" },
    },
    { key: "end", type: "end", next: [] },
  ],
};

describe("workflow builder protocol", () => {
  it("encodes only the request and available Codex agents", () => {
    const encoded = encodeWorkflowBuilderInput("Ship a release", [
      { id: "agent-1", name: "Engineer", description: "Builds releases" },
    ]);
    expect(encoded).toContain('"user_request": "Ship a release"');
    expect(encoded).toContain('"id": "agent-1"');
    expect(encoded).toContain('"name": "Engineer"');
  });

  it("parses and normalizes a structured workflow draft", () => {
    const parsed = parseWorkflowBuilderDraft(
      `Here is the plan.
<workflow_draft>${JSON.stringify({
        name: " Release Flow ",
        key: "RELEASE_FLOW",
        description: " Ship safely. ",
        definition: validDefinition,
      })}</workflow_draft>`,
    );
    expect(parsed?.name).toBe("Release Flow");
    expect(parsed?.key).toBe("release_flow");
    expect(parsed?.definition.nodes.map((node) => node.key)).toEqual([
      "input",
      "work",
      "end",
    ]);
  });

  it("recovers a draft from fenced or untagged Codex JSON", () => {
    const draft = {
      name: "需求实现与验收",
      key: "requirement_delivery",
      description: "拆分、实现并验收需求。",
      definition: validDefinition,
    };
    expect(
      parseWorkflowBuilderDraft(
        `草稿如下：\n\`\`\`json\n${JSON.stringify(draft)}\n\`\`\``,
      )?.key,
    ).toBe("requirement_delivery");
    expect(
      parseWorkflowBuilderDraft(`已完成。\n${JSON.stringify(draft)}`)?.name,
    ).toBe("需求实现与验收");
  });

  it("parses a workflow_draft JSON wrapper", () => {
    const parsed = parseWorkflowBuilderDraft(
      JSON.stringify({
        workflow_draft: {
          name: "Delivery",
          key: "delivery",
          definition: validDefinition,
        },
      }),
    );
    expect(parsed?.key).toBe("delivery");
  });

  it("encodes a bounded format repair with the original inputs", () => {
    const repair = encodeWorkflowBuilderRepairInput("拆分并实现需求", [
      { id: "agent-1", name: "分析", description: "拆分需求" },
    ]);
    expect(repair).toContain("MULTICA_WORKFLOW_BUILDER_FORMAT_REPAIR_V1");
    expect(repair).toContain("<workflow_draft>");
    expect(repair).toContain("拆分并实现需求");
    expect(repair).toContain("agent-1");
  });

  it("normalizes the compact node aliases returned by live Codex", () => {
    const parsed = parseWorkflowBuilderDraft(
      `<workflow_draft>${JSON.stringify({
        name: "需求拆分、实现与验收",
        key: "requirement-delivery",
        description: "拆分、实现并验证需求，失败时返工。",
        definition: {
          schema_version: 1,
          entry_node: "input",
          nodes: [
            {
              id: "input",
              type: "input",
              next: "analyze",
              input_fields: [
                { key: "task", type: "textarea", required: true },
              ],
            },
            {
              id: "analyze",
              type: "agent",
              agent_id: "agent-1",
              instruction: "拆分需求。",
              next: "verify",
            },
            {
              id: "verify",
              type: "agent",
              agent_id: "agent-2",
              instruction: "运行功能测试。",
              next: "acceptance",
              on_failure: "rework",
              rework_target: "analyze",
            },
            {
              id: "acceptance",
              type: "acceptance",
              next: "end",
              acceptance_criteria: "功能已经完成并且测试通过。",
              rework_targets: ["analyze", "verify"],
            },
            { id: "end", type: "end" },
          ],
        },
      })}</workflow_draft>`,
    );

    expect(parsed?.definition.nodes.map((node) => node.key)).toEqual([
      "input",
      "analyze",
      "verify",
      "acceptance",
      "end",
    ]);
    expect(parsed?.definition.nodes[0]?.next).toEqual(["analyze"]);
    expect(parsed?.definition.nodes[1]?.routing).toMatchObject({
      strategy: "explicit",
      agent_id: "agent-1",
    });
    expect(parsed?.definition.nodes[2]?.rework_targets).toEqual(["analyze"]);
    expect(parsed?.definition.nodes[3]?.acceptance_criteria).toEqual([
      "功能已经完成并且测试通过。",
    ]);
  });

  it("rejects invalid keys and malformed definitions", () => {
    expect(
      parseWorkflowBuilderDraft(
        `<workflow_draft>${JSON.stringify({
          name: "Bad",
          key: "bad key",
          definition: validDefinition,
        })}</workflow_draft>`,
      ),
    ).toBeNull();
    expect(
      parseWorkflowBuilderDraft(
        '<workflow_draft>{"name":"Bad","key":"bad","definition":{"nodes":"nope"}}</workflow_draft>',
      ),
    ).toBeNull();
  });
});
