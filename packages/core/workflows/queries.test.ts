import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, setApiInstance } from "../api";
import type { WorkflowTemplateDetail } from "./schemas";
import { workflowTemplateDetailOptions, workflowTemplateRunOptions } from "./queries";

describe("workflowTemplateDetailOptions", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("loads the editable draft view for the workflow editor", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response("{}", {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    setApiInstance(new ApiClient("https://api.example.test"));
    const options = workflowTemplateDetailOptions("ws-1", "wft-1");

    await (options.queryFn as () => Promise<WorkflowTemplateDetail>)();

    const url = String(vi.mocked(fetch).mock.calls[0]?.[0]);
    expect(url).toBe(
      "https://api.example.test/api/workflow-templates/wft-1?definition=draft",
    );
  });
});

it("fetches the effective publication in a separate workspace and version cache", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("{}", { status: 200 })));
  try {
    setApiInstance(new ApiClient("https://api.example.test"));
    const options = workflowTemplateRunOptions("ws-1", "wft-1", 2);
    await (options.queryFn as () => Promise<WorkflowTemplateDetail>)();
    expect(String(vi.mocked(fetch).mock.calls[0]?.[0])).toBe("https://api.example.test/api/workflow-templates/wft-1");
    expect(options.queryKey).not.toEqual(workflowTemplateDetailOptions("ws-1", "wft-1").queryKey);
    expect(options.queryKey).not.toEqual(workflowTemplateRunOptions("ws-1", "wft-1", 1).queryKey);
    expect(options.queryKey).not.toEqual(workflowTemplateRunOptions("ws-2", "wft-1", 2).queryKey);
  } finally {
    vi.unstubAllGlobals();
  }
});
