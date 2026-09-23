// @vitest-environment jsdom
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { runRuntimeCLI } from "@multica/core/clis";
import type { RuntimeCLIRunRequest, RuntimeCLISummary } from "@multica/core/types";
import zhClis from "../../locales/zh-Hans/clis.json";
import enClis from "../../locales/en/clis.json";
import { CLIRunSection } from "./cli-run-section";

vi.mock("@multica/core/clis", async (importOriginal) => ({
  ...await importOriginal<typeof import("@multica/core/clis")>(),
  runRuntimeCLI: vi.fn(),
}));

const entry: RuntimeCLISummary = {
  key: "zhihu-search", label: "知乎搜索", available: true,
  timeout_seconds: 30, max_output_bytes: 65536,
  params: [{ name: "query", type: "string", required: true }],
};
const run: RuntimeCLIRunRequest = {
  id: "run-1", runtime_id: "runtime-1", cli_key: "zhihu-search",
  status: "completed", exit_code: 0, created_at: "", updated_at: "",
  output: JSON.stringify({ Code: 0, Data: { Items: [{ Title: "Search result" }] } }),
};

function renderSections(secondEntry = false) {
  return render(
    <I18nProvider locale="zh-Hans" resources={{ "zh-Hans": { clis: zhClis }, en: { clis: enClis } }}>
      <section aria-label="search"><CLIRunSection runtimeId="runtime-1" entry={entry} /></section>
      {secondEntry && <section aria-label="other"><CLIRunSection runtimeId="runtime-1" entry={{ ...entry, key: "other", params: [] }} /></section>}
    </I18nProvider>,
  );
}

beforeEach(() => {
  vi.mocked(runRuntimeCLI).mockReset();
});

describe("CLIRunSection result controls", () => {
  it("collapses and restores results without rerunning or losing expanded details", async () => {
    vi.mocked(runRuntimeCLI).mockResolvedValue(run);
    renderSections();
    expect(screen.queryByRole("button", { name: "收起结果" })).toBeNull();
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "zhihu cli" } });
    fireEvent.click(screen.getByRole("button", { name: "运行" }));
    const heading = await screen.findByRole("heading", { name: "Search result" });
    fireEvent.click(screen.getByText("查看原始输出"));
    const raw = screen.getByText("查看原始输出").closest("details");
    const collapse = screen.getByRole("button", { name: "收起结果" });
    expect(collapse).toHaveAttribute("aria-expanded", "true");
    expect(document.getElementById(collapse.getAttribute("aria-controls")!)).toContainElement(heading);
    fireEvent.click(collapse);
    expect(heading).not.toBeVisible();
    expect(screen.getByText("运行详情")).not.toBeVisible();
    expect(screen.getByRole("button", { name: "清除结果" })).toBeVisible();
    const expand = screen.getByRole("button", { name: "展开结果" });
    expect(expand).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(expand);
    expect(heading).toBeVisible();
    expect(raw).toHaveAttribute("open");
    expect(screen.getByRole("textbox")).toHaveValue("zhihu cli");
    expect(runRuntimeCLI).toHaveBeenCalledTimes(1);
  });

  it("expands new results automatically and can clear while collapsed", async () => {
    vi.mocked(runRuntimeCLI).mockResolvedValue(run);
    renderSections();
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "query" } });
    fireEvent.click(screen.getByRole("button", { name: "运行" }));
    await screen.findByRole("heading", { name: "Search result" });
    fireEvent.click(screen.getByRole("button", { name: "收起结果" }));
    fireEvent.click(screen.getByRole("button", { name: "运行" }));
    expect(await screen.findByRole("heading", { name: "Search result" })).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "收起结果" }));
    fireEvent.click(screen.getByRole("button", { name: "清除结果" }));
    expect(screen.queryByRole("button", { name: "展开结果" })).toBeNull();
    expect(screen.queryByText("Search result")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "运行" }));
    expect(await screen.findByRole("heading", { name: "Search result" })).toBeVisible();
  });

  it("clears the full result without changing parameters or running another command", async () => {
    vi.mocked(runRuntimeCLI).mockResolvedValue(run);
    renderSections();
    expect(screen.queryByRole("button", { name: "清除结果" })).toBeNull();
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "zhihu cli" } });
    fireEvent.click(screen.getByRole("button", { name: "运行" }));
    await screen.findByRole("heading", { name: "Search result" });
    fireEvent.click(screen.getByRole("button", { name: "清除结果" }));
    expect(screen.queryByRole("heading", { name: "Search result" })).toBeNull();
    expect(screen.queryByText("查看原始输出")).toBeNull();
    expect(screen.queryByText("运行详情")).toBeNull();
    expect(screen.queryByRole("button", { name: "清除结果" })).toBeNull();
    expect(screen.getByRole("textbox")).toHaveValue("zhihu cli");
    expect(runRuntimeCLI).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "运行" }));
    await screen.findByRole("heading", { name: "Search result" });
    expect(runRuntimeCLI).toHaveBeenLastCalledWith("runtime-1", "zhihu-search", {
      params: { query: "zhihu cli" }, timeout_seconds: 30,
    });
  });

  it("allows clearing an error after completion but not an in-flight run", async () => {
    let rejectRun!: (error: Error) => void;
    vi.mocked(runRuntimeCLI).mockImplementation(() => new Promise((_, reject) => { rejectRun = reject; }));
    renderSections();
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "query" } });
    fireEvent.click(screen.getByRole("button", { name: "运行" }));
    expect(screen.getByRole("button", { name: "运行中…" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "清除结果" })).toBeNull();
    expect(screen.queryByRole("button", { name: "收起结果" })).toBeNull();
    await act(async () => rejectRun(new Error("CLI unavailable")));
    expect(screen.getByText("CLI unavailable")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "收起结果" }));
    expect(screen.getByText("CLI unavailable")).not.toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "展开结果" }));
    expect(screen.getByText("CLI unavailable")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "清除结果" }));
    expect(screen.queryByText("CLI unavailable")).toBeNull();
    expect(screen.getByRole("textbox")).toHaveValue("query");
  });

  it("only clears the selected CLI entry", async () => {
    vi.mocked(runRuntimeCLI).mockResolvedValueOnce(run).mockResolvedValueOnce({ ...run, cli_key: "other", output: "Other result" });
    renderSections(true);
    const search = within(screen.getByRole("region", { name: "search" }));
    const other = within(screen.getByRole("region", { name: "other" }));
    fireEvent.change(search.getByRole("textbox"), { target: { value: "query" } });
    fireEvent.click(search.getByRole("button", { name: "运行" }));
    await search.findByRole("heading", { name: "Search result" });
    fireEvent.click(other.getByRole("button", { name: "运行" }));
    await other.findByText("Other result");
    fireEvent.click(search.getByRole("button", { name: "收起结果" }));
    expect(other.getByText("Other result")).toBeVisible();
    expect(other.getByRole("button", { name: "收起结果" })).toBeVisible();
    fireEvent.click(search.getByRole("button", { name: "清除结果" }));
    expect(search.queryByRole("heading", { name: "Search result" })).toBeNull();
    expect(other.getByText("Other result")).toBeVisible();
  });
});
