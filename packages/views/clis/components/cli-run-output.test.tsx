// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { RuntimeCLIRunRequest } from "@multica/core/types";
import zhClis from "../../locales/zh-Hans/clis.json";
import enClis from "../../locales/en/clis.json";
import { CLIRunOutput } from "./cli-run-output";

function renderOutput(value: unknown, overrides: Partial<RuntimeCLIRunRequest> = {}) {
  const run: RuntimeCLIRunRequest = {
    id: "run-1", runtime_id: "runtime-1", cli_key: "zhihu-search", status: "completed", exit_code: 0,
    created_at: "", updated_at: "", output: JSON.stringify(value), ...overrides,
  };
  return render(<I18nProvider locale="zh-Hans" resources={{ "zh-Hans": { clis: zhClis }, en: { clis: enClis } }}><CLIRunOutput run={run} /></I18nProvider>);
}

describe("CLIRunOutput", () => {
  it("renders the hot list with numbered topics, summaries and original links", () => {
    renderOutput({ Code: 0, Data: { Total: 2, Items: [
      { Title: "First topic", Summary: "A topic summary", Url: "https://www.zhihu.com/question/1" },
      { Title: "Second topic", Summary: "Another summary" },
    ] } }, { cli_key: "zhihu-hot" });
    expect(screen.getByRole("heading", { name: "知乎热榜 · 2 条" })).toBeVisible();
    expect(screen.getByText("第 1 名")).toBeVisible();
    expect(screen.getByText("第 2 名")).toBeVisible();
    expect(screen.getByText("A topic summary")).toBeVisible();
    expect(screen.getByRole("link", { name: "查看原文" })).toHaveAttribute("href", "https://www.zhihu.com/question/1");
    expect(screen.getByText("查看原始输出").closest("details")).not.toHaveAttribute("open");
  });

  it("shows a hot-list-specific empty state", () => {
    renderOutput({ Code: 0, Data: { Items: [] } }, { cli_key: "zhihu-hot" });
    expect(screen.getByText("暂无热榜内容，请稍后重试。")).toBeVisible();
  });

  it("labels hot-list failures without calling them search failures", () => {
    renderOutput({ Code: 20001, Message: "Authentication failed" }, { cli_key: "zhihu-hot" });
    expect(screen.getByRole("alert")).toHaveTextContent("获取热榜失败（错误码 20001）。");
  });

  it("shows readable results, expands the returned excerpt and keeps raw output collapsed", () => {
    const text = "A long excerpt. ".repeat(30) + "END OF EXCERPT";
    renderOutput({ Code: 0, Data: { Items: [{ Title: "Zhihu CLI", AuthorName: "An author", ContentType: "Article", ContentText: text, Url: "https://zhuanlan.zhihu.com/p/1", VoteUpCount: 182, CommentCount: 22 }] } });
    expect(screen.getByText("共 1 条结果")).toBeVisible();
    expect(screen.getByText("An author")).toBeVisible();
    expect(screen.getByText("182 赞同")).toBeVisible();
    const link = screen.getByRole("link", { name: "查看原文" });
    expect(link).toHaveAttribute("href", "https://zhuanlan.zhihu.com/p/1");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    const expand = screen.getByRole("button", { name: "展开摘录" });
    expect(expand).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(expand);
    expect(screen.getByText(text)).toBeVisible();
    expect(screen.getByRole("button", { name: "收起" })).toHaveAttribute("aria-expanded", "true");
    const raw = screen.getByText("查看原始输出").closest("details");
    expect(raw).not.toHaveAttribute("open");
    fireEvent.click(screen.getByText("查看原始输出"));
    expect(raw).toHaveAttribute("open");
  });

  it("renders a status dashboard including the credential source", () => {
    renderOutput({ ok: true, installed: true, next_action: "ready", auth: { configured: true, source: "keychain" }, cli: { current_version: "0.6.0", compatible: true, update_available: false }, skill: { current_version: "0.7.1", update_available: false }, update_check: { status: "verified" } }, { cli_key: "zhihu" });
    expect(screen.getByRole("heading", { name: "知乎 CLI 已就绪" })).toBeVisible();
    for (const text of ["0.6.0", "0.7.1", "系统凭据库", "已验证"]) expect(screen.getByText(text)).toBeVisible();
  });

  it("gives setup guidance instead of reporting ready when authentication is missing", () => {
    renderOutput({ ok: true, installed: true, next_action: "request_access_secret", auth: { configured: false }, cli: { current_version: "0.6.0", compatible: true } }, { cli_key: "zhihu" });
    expect(screen.getByText("请配置 Access Secret 后重新检查。")).toBeVisible();
    expect(screen.queryByRole("heading", { name: "知乎 CLI 已就绪" })).toBeNull();
    expect(screen.getByText("未验证")).toBeVisible();
  });

  // Malformed payloads and URL validation are covered by core/clis/zhihu-output.test.ts.
  it("renders unsafe source content as text without creating links or HTML", () => {
    const { container } = renderOutput({ Code: 0, Data: { Items: [{ Title: "<img src=x onerror=alert(1)>", ContentText: "<script>alert(1)</script>", Url: "javascript:alert(1)" }] } });
    expect(screen.queryByRole("link")).toBeNull();
    expect(container.querySelector("img,script")).toBeNull();
    expect(screen.getByText("<script>alert(1)</script>")).toBeVisible();
  });

  it("shows an API failure as an error, not an empty successful search", () => {
    renderOutput({ Code: 20001, Message: "Authentication failed" });
    expect(screen.getByRole("alert")).toHaveTextContent("20001");
    expect(screen.queryByText("没有找到相关内容，请尝试其他关键词。")).toBeNull();
  });

  it("keeps truncated output visible with a warning", () => {
    renderOutput(null, { output: '{"Code":0,"Data":', truncated: true, output_bytes: 70000 });
    expect(screen.getByText('{"Code":0,"Data":')).toBeVisible();
    expect(screen.getByRole("status")).toHaveTextContent("70000");
  });
});
