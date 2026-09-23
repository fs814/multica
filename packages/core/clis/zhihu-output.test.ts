// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { RuntimeCLIRunRequest } from "../types/cli";
import { parseZhihuOutput } from "./zhihu-output";

function run(value: unknown, overrides: Partial<RuntimeCLIRunRequest> = {}): RuntimeCLIRunRequest {
  return { id: "1", runtime_id: "1", cli_key: "zhihu-search", status: "completed", exit_code: 0,
    created_at: "", updated_at: "", output: JSON.stringify(value), ...overrides };
}
const search = (item: unknown) => ({ Code: 0, Data: { Items: [item], HasMore: false } });

describe("parseZhihuOutput", () => {
  it("normalizes hot-list summaries and preserves the API order", () => {
    const output = parseZhihuOutput(run({ Code: 0, Data: { Total: 2, Items: [
      { Title: "First topic", Summary: "Topic summary", Url: "https://www.zhihu.com/question/1", ThumbnailUrl: "https://example.com/image.jpg" },
      { Title: "Second topic", Summary: null, Url: "javascript:alert(1)" },
    ] } }, { cli_key: "zhihu-hot" }));
    expect(output).toEqual({ kind: "hot", items: [
      { title: "First topic", text: "Topic summary", url: "https://www.zhihu.com/question/1" },
      { title: "Second topic", text: "", url: null },
    ] });
  });

  it("distinguishes empty hot lists, API errors and malformed responses", () => {
    const hotRun = (value: unknown) => run(value, { cli_key: "zhihu-hot" });
    expect(parseZhihuOutput(hotRun({ Code: 0, Data: { Items: [] } }))).toEqual({ kind: "hot", items: [] });
    expect(parseZhihuOutput(hotRun({ Code: 20001, Message: "Authentication failed" }))).toEqual({ kind: "error", code: 20001, message: "Authentication failed" });
    expect(parseZhihuOutput(hotRun({ Code: 0, Data: { Items: [{ Summary: "Missing title" }] } }))).toBeNull();
    expect(parseZhihuOutput(hotRun({ Code: 0, Data: {} }))).toBeNull();
  });

  it("normalizes search metadata and preserves content as plain text", () => {
    const output = parseZhihuOutput(run(search({ Title: "A title", ContentID: "-9188998394482887318", ContentText: "<script>alert(1)</script>", Url: "https://www.zhihu.com/question/1", VoteUpCount: 0 })));
    expect(output).toEqual({ kind: "search", hasMore: false, items: [{ title: "A title", text: "<script>alert(1)</script>", url: "https://www.zhihu.com/question/1", votes: 0 }] });
  });

  it.each(["javascript:alert(1)", "file:///C:/secret.txt", "data:text/html,test", "//example.com", "https://user:pass@example.com", "bad url"])("does not make %s clickable", (url) => {
    const parsed = parseZhihuOutput(run(search({ Title: "Title", Url: url })));
    expect(parsed?.kind === "search" && parsed.items[0]?.url).toBeNull();
  });

  it("keeps global search web links and tolerates missing or malformed optional fields", () => {
    const parsed = parseZhihuOutput(run(search({ Title: "Web result", Url: "https://example.com/page", VoteUpCount: "lots", AuthorName: {}, ContentText: null })));
    expect(parsed?.kind === "search" && parsed.items[0]).toMatchObject({ title: "Web result", url: "https://example.com/page", text: "", votes: undefined });
  });

  it("distinguishes an empty search from an API failure even with exit code zero", () => {
    expect(parseZhihuOutput(run({ Code: 0, Data: { Items: [] } }))).toEqual({ kind: "search", items: [], hasMore: false });
    expect(parseZhihuOutput(run({ Code: 20001, Message: "Authentication failed" }))).toEqual({ kind: "error", code: 20001, message: "Authentication failed" });
  });

  it.each([
    { output: "broken JSON" }, { output: JSON.stringify({ Code: 0, Data: { Items: [null] } }) },
    { cli_key: "another-cli" }, { truncated: true }, { exit_code: 1 }, { status: "failed" as const },
  ])("falls back to raw output for %j", (overrides) => {
    expect(parseZhihuOutput(run(search({ Title: "Title" }), overrides))).toBeNull();
  });

  it("parses status without treating unknown checks as verified", () => {
    const output = parseZhihuOutput(run({ ok: true, installed: true, auth: { configured: true, source: "keychain" }, cli: { current_version: "0.6.0" } }, { cli_key: "zhihu" }));
    expect(output?.kind).toBe("status");
    if (output?.kind === "status") {
      expect(output.data.cli.compatible).toBeUndefined();
      expect(output.data.update_check).toBeUndefined();
    }
  });
});
