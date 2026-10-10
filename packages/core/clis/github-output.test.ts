// @vitest-environment node
import { describe, expect, it } from "vitest";
import { parseGitHubTrendingOutput } from "./github-output";
import type { RuntimeCLIRunRequest } from "../types/cli";

const payload = { since: "daily", repositories: [
  { repository: "owner/repo", description: "<script>example</script>", language: "Go", stars: "123", gained: "10 stars today", url: "javascript:alert(1)" },
] };
function run(output: unknown, overrides: Partial<RuntimeCLIRunRequest> = {}): RuntimeCLIRunRequest {
  return { id: "run", runtime_id: "runtime", cli_key: "gh-trending", status: "completed", exit_code: 0,
    output: JSON.stringify(output), created_at: "", updated_at: "", ...overrides };
}
describe("parseGitHubTrendingOutput", () => {
  it("preserves ranking and derives safe repository links", () => {
    const parsed = parseGitHubTrendingOutput(run(payload));
    expect(parsed?.repositories[0]).toMatchObject({ repository: "owner/repo", url: "https://github.com/owner/repo", stars: "123" });
  });
  it.each([
    { since: "unknown", repositories: [] },
    { since: "daily", repositories: [{ repository: "javascript:alert(1)" }] },
    { since: "daily", repositories: "bad" },
    null,
  ])("rejects malformed output %j", (output) => {
    expect(parseGitHubTrendingOutput(run(output))).toBeNull();
  });
  it.each([{ cli_key: "other" }, { status: "failed" as const }, { exit_code: 1 }, { truncated: true }, { output: "not JSON" }])("leaves unsupported runs as raw output %j", (overrides) => {
    expect(parseGitHubTrendingOutput(run(payload, overrides))).toBeNull();
  });
});
