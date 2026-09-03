import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiClient, setApiInstance } from "../api";
import type { WorkflowTemplateDetail } from "./schemas";
import { workflowTemplateDetailOptions } from "./queries";

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
