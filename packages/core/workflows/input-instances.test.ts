import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api";
import { WorkflowNodeSchema } from "./schemas";
import { WorkflowInputInstanceSchema } from "./input-instance-schemas";
import { groupWorkflowInstances, workflowInputInstanceKeys } from "./input-instances";

const row = {
  id: "instance-a",
  template_id: "template-a",
  name: "Scenario A",
  input: { title: "Task", description: "A", custom: "value" },
  project_id: null,
  revision: 1,
};
afterEach(() => vi.unstubAllGlobals());
const respond = (data: unknown) =>
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify(data), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
  );
describe("workflow input instances", () => {
  it("parses instances without losing authored inputs", async () => {
    respond({ instances: [row] });
    const rows = await new ApiClient(
      "https://api.example.test",
    ).listWorkflowInputInstances("template-a");
    expect(rows[0]).toMatchObject({
      templateId: "template-a",
      input: row.input,
      projectId: null,
    });
  });
  it("fails visibly on malformed responses", async () => {
    for (const data of [
      {},
      { instances: null },
      { instances: [{ ...row, input: { value: 4 } }] },
    ]) {
      respond(data);
      await expect(
        new ApiClient("https://api.example.test").listWorkflowInputInstances(
          "template-a",
        ),
      ).rejects.toThrow("Unable to read");
    }
  });
  it("writes revision and project using the wire contract", async () => {
    respond(row);
    await new ApiClient("https://api.example.test").saveWorkflowInputInstance(
      "template-a",
      {
        name: "Scenario A",
        input: row.input,
        projectId: "project-a",
        revision: 1,
      },
      "instance-a",
    );
    const [url, init] = vi.mocked(fetch).mock.calls[0]!;
    expect(String(url)).toContain("/template-a/input-instances/instance-a");
    expect(init?.method).toBe("PUT");
    expect(JSON.parse(String(init?.body))).toEqual({
      name: "Scenario A",
      input: row.input,
      project_id: "project-a",
      revision: 1,
    });
  });
  it("isolates caches by workspace and workflow", () => {
    expect(workflowInputInstanceKeys.list("one", "a")).not.toEqual(
      workflowInputInstanceKeys.list("two", "a"),
    );
    expect(workflowInputInstanceKeys.list("one", "a")).not.toEqual(
      workflowInputInstanceKeys.list("one", "b"),
    );
  });
});
it("pins the displayed run version outside the input bag", async () => {
  respond({});
  await new ApiClient("https://api.example.test").runWorkflowTemplate(
    "template-a",
    {
      title: "Task",
      description: "Input",
      templateVersionId: "version-a",
    },
  );
  const [, init] = vi.mocked(fetch).mock.calls[0]!;
  expect(new Headers(init?.headers).get("X-Workflow-Template-Version-ID")).toBe(
    "version-a",
  );
  expect(JSON.parse(String(init?.body))).toEqual({
    title: "Task",
    description: "Input",
  });
});

it("roundtrips instance declarations, image references and provenance", async () => {
  respond({
    ...row,
    input_node: {
      key: "input",
      type: "input",
      input_mode: "image",
      image_attachment_id: "image-a",
    },
    image_attachment_id: "image-b",
    template_version_id: "v1",
    archived_at: null,
    updated_by_id: "editor",
  });
  const value = await new ApiClient(
    "https://api.example.test",
  ).getWorkflowInstance("instance-a");
  expect(value).toMatchObject({
    input: row.input,
    imageAttachmentId: "image-b",
    templateVersionId: "v1",
    updatedById: "editor",
    inputNode: { input_mode: "image" },
  });
});
it("rejects malformed managed-instance reads and run lists", async () => {
  const client = new ApiClient("https://api.example.test");
  for (const data of [{}, null, { runs: null, total: 0 }]) {
    respond(data);
    await expect(client.getWorkflowInstance("a")).rejects.toThrow(
      "Unable to read",
    );
    respond(data);
    await expect(client.listWorkflowInstanceRuns("a")).rejects.toThrow(
      "Unable to read",
    );
  }
});
it("keeps saved run intent separate from temporary inputs", async () => {
  respond({ id: "run", status: "running" });
  const client = new ApiClient("https://api.example.test");
  await client.runWorkflowInstance("a", {
    mode: "saved",
    revision: 3,
    idempotency_key: "retry",
  });
  const [, init] = vi.mocked(fetch).mock.calls[0]!;
  expect(JSON.parse(String(init?.body))).toEqual({
    mode: "saved",
    revision: 3,
    idempotency_key: "retry",
  });
});

it("sends the complete create-instance form using the server field names", async () => {
  respond(row);
  const inputNode = WorkflowNodeSchema.parse({
    key: "input", type: "input", name: "Task", instruction: "Input A",
    input_fields: [], next: ["end"],
  });
  await new ApiClient("https://api.example.test").saveWorkflowInputInstance("template-a", {
    name: "Scenario A",
    description: "Reusable input scenario",
    input: row.input,
    inputNode,
    projectId: null,
    templateVersionId: null,
    imageAttachmentId: "",
    idempotencyKey: "create-attempt-a",
  });
  const [url, init] = vi.mocked(fetch).mock.calls[0]!;
  expect(String(url)).toContain("/template-a/input-instances");
  expect(init?.method).toBe("POST");
  expect(JSON.parse(String(init?.body))).toEqual({
    name: "Scenario A", description: "Reusable input scenario", input: row.input,
    input_node: inputNode, project_id: null, template_version_id: null,
    image_attachment_id: "", idempotency_key: "create-attempt-a",
  });
});

it("groups by parent ID even when workflows share a name or are absent from the template list", () => {
  const instance = WorkflowInputInstanceSchema.parse(row);
  const a1 = { ...instance, id: "a1", templateId: "a", templateName: "Same name" };
  const b1 = { ...instance, id: "b1", templateId: "b", templateName: "Same name" };
  const a2 = { ...instance, id: "a2", templateId: "a", templateName: "Same name" };
  const groups = groupWorkflowInstances([a1, b1, a2]);
  expect(groups).toEqual([
    { templateId: "a", name: "Same name", instances: [a1, a2] },
    { templateId: "b", name: "Same name", instances: [b1] },
  ]);
  expect(groupWorkflowInstances([a1], [{ id: "a", name: "Renamed workflow" }])[0]?.name).toBe("Renamed workflow");
});

it("passes a single-step history intent without overriding its input snapshot", async () => {
 respond({id:"run",status:"running"});
 const body = {mode:"history" as const,revision:3,history_run_id:"original",script_step:"build" as const,idempotency_key:"build-only"};
 await new ApiClient("https://api.example.test").runWorkflowInstance("a",body);
 expect(JSON.parse(String(vi.mocked(fetch).mock.calls[0]![1]?.body))).toEqual(body);
});
