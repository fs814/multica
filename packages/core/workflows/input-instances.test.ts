import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient } from "../api";
import { workflowInputInstanceKeys } from "./input-instances";

const row = { id: "instance-a", template_id: "template-a", name: "Scenario A", input: { title: "Task", description: "A", custom: "value" }, project_id: null, revision: 1 };
afterEach(() => vi.unstubAllGlobals());
const respond = (data: unknown) => vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(data), { status: 200, headers: { "Content-Type": "application/json" } })));
describe("workflow input instances", () => {
  it("parses instances without losing authored inputs", async () => {
    respond({ instances: [row] });
    const rows = await new ApiClient("https://api.example.test").listWorkflowInputInstances("template-a");
    expect(rows[0]).toMatchObject({ templateId: "template-a", input: row.input, projectId: null });
  });
  it("fails visibly on malformed responses", async () => {
    for (const data of [{}, { instances: null }, { instances: [{ ...row, input: { value: 4 } }] }]) {
      respond(data);
      await expect(new ApiClient("https://api.example.test").listWorkflowInputInstances("template-a")).rejects.toThrow("Unable to read");
    }
  });
  it("writes revision and project using the wire contract", async () => {
    respond(row);
    await new ApiClient("https://api.example.test").saveWorkflowInputInstance("template-a", { name: "Scenario A", input: row.input, projectId: "project-a", revision: 1 }, "instance-a");
    const [url, init] = vi.mocked(fetch).mock.calls[0]!;
    expect(String(url)).toContain("/template-a/input-instances/instance-a");
    expect(init?.method).toBe("PUT");
    expect(JSON.parse(String(init?.body))).toEqual({ name: "Scenario A", input: row.input, project_id: "project-a", revision: 1 });
  });
  it("isolates caches by workspace and workflow", () => {
    expect(workflowInputInstanceKeys.list("one", "a")).not.toEqual(workflowInputInstanceKeys.list("two", "a"));
    expect(workflowInputInstanceKeys.list("one", "a")).not.toEqual(workflowInputInstanceKeys.list("one", "b"));
  });
});
it("pins the displayed run version outside the input bag", async () => {
  respond({});
  await new ApiClient("https://api.example.test").runWorkflowTemplate("template-a", {
    title: "Task", description: "Input", templateVersionId: "version-a",
  });
  const [, init] = vi.mocked(fetch).mock.calls[0]!;
  expect(new Headers(init?.headers).get("X-Workflow-Template-Version-ID")).toBe("version-a");
  expect(JSON.parse(String(init?.body))).toEqual({ title: "Task", description: "Input" });
});
