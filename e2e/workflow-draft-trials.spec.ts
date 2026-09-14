import {
  test,
  expect,
  _electron as electron,
  type Page,
  type TestInfo,
} from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { mkdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import pg from "pg";

const graph = (name: string) => ({
  schema_version: 2,
  entry_node: "input",
  nodes: [
    {
      key: "input",
      type: "input",
      name: "Trial input",
      next: ["review"],
      next_ids: ["ir"],
      input_fields: [
        { key: "title", type: "text", label: "Title", required: true },
        {
          key: "description",
          type: "textarea",
          label: "Description",
          required: true,
        },
      ],
    },
    {
      key: "review",
      type: "acceptance",
      name,
      acceptance_criteria: ["Check the immutable draft snapshot"],
      next: ["end"],
      next_ids: ["re"],
    },
    { key: "end", type: "end", name: "Finished" },
  ],
});
const caps = [
  "workflow_debug_stop_receipt_v1",
  "workflow_debug_fixed_environment_v1",
];

async function exercise(page: Page, info: TestInfo, desktop: boolean) {
  test.setTimeout(180000);
  page.setDefaultTimeout(20000);
  await page.setViewportSize({ width: desktop ? 1440 : 1280, height: 900 });
  await expect
    .poll(
      async () => {
        try {
          return (await fetch(process.env.NEXT_PUBLIC_API_URL!)).status < 500;
        } catch {
          return false;
        }
      },
      { timeout: 30000 },
    )
    .toBe(true);
  const api = new TestApiClient();
  await api.login(`trial-${Date.now()}@example.com`, "Trial verifier");
  const ws = await api.ensureWorkspace(
    "Draft trial verification",
    `trial-${Date.now()}`,
  );
  await api.markUserOnboarded();
  const login = (token: string | null) => {
    localStorage.setItem("multica_token", token!);
    localStorage.setItem("multica:chat:isOpen", "false");
    localStorage.setItem("i18nextLng", "en");
  };
  await page.addInitScript(login, api.getToken());
  if (desktop) {
    await page.evaluate(login, api.getToken());
    await page.reload();
  }
  const navigate = async (path: string) => {
    if (desktop)
      await page.evaluate(
        (path) =>
          window.dispatchEvent(
            new CustomEvent("multica:navigate", { detail: { path } }),
          ),
        path,
      );
    else await page.goto(path);
  };
  const settings = await api.workflowRequest(
    "/api/workflow-test-runs/settings",
  );
  expect(settings.settings.enabled).toBe(false);
  const template = await api.workflowRequest("/api/workflow-templates", {
    method: "POST",
    body: JSON.stringify({
      key: `trial-${Date.now()}`,
      name: "Draft trial template",
      definition: graph("Saved graph A"),
    }),
  });
  await navigate(`/${ws.slug}/workflows/${template.id}`);
  await expect(
    page.getByRole("button", { name: "Try draft", exact: true }),
  ).toHaveCount(0);
  await page
    .getByRole("button", { name: "Trial settings", exact: true })
    .click();
  const settingsDialog = page.getByRole("dialog");
  await settingsDialog.getByLabel("Allow new draft trials").check();
  await settingsDialog
    .getByRole("button", { name: "Save settings", exact: true })
    .click();
  await settingsDialog
    .getByRole("button", { name: "Close", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Try draft", exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Toggle the JSON view", exact: true })
    .click();
  const json = page.getByRole("textbox", {
    name: /Workflow.*JSON|JSON.*workflow/i,
  });
  await json.fill(JSON.stringify(graph("Unsaved graph B"), null, 2));
  await expect(
    page.getByRole("button", { name: "Try draft", exact: true }),
  ).toBeDisabled();
  await page.getByRole("button", { name: "Apply", exact: true }).click();
  await page.getByRole("button", { name: "Try draft", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(/not a sandbox/)).toBeVisible();
  await dialog
    .getByRole("textbox", { name: "Title", exact: true })
    .fill("Controlled trial B");
  await dialog
    .getByRole("textbox", { name: "Description", exact: true })
    .fill("Immutable input for real trial. ".repeat(100));
  await dialog
    .getByRole("textbox", { name: "Description", exact: true })
    .press("Tab");
  await expect(
    dialog.getByRole("textbox", { name: "Description", exact: true }),
  ).not.toBeFocused();
  await expect(
    dialog.getByRole("button", { name: "Confirm and run" }),
  ).toBeDisabled();
  await dialog.getByRole("checkbox").focus();
  await page.keyboard.press("Space");
  await expect(dialog.getByRole("checkbox")).toBeChecked();
  const longDescription = dialog.getByRole("textbox", {
    name: "Description",
    exact: true,
  });
  const descriptionHeight = await longDescription.evaluate(
    (el) => el.getBoundingClientRect().height,
  );
  expect(descriptionHeight).toBeLessThanOrEqual(
    (page.viewportSize()?.height ?? 900) / 4,
  );
  await expect(dialog.getByText(/not a sandbox/)).toBeInViewport();
  await expect(
    dialog.getByRole("button", { name: "Confirm and run" }),
  ).toBeInViewport();
  await page.screenshot({
    path: info.outputPath("trial-warning-light.png"),
    fullPage: true,
  });
  const response = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      r.url().endsWith(`/workflow-templates/${template.id}/test-runs`),
  );
  await dialog.getByRole("button", { name: "Confirm and run" }).click();
  const payload = await (await response).json();
  expect(payload.execution_ref.kind).toBe("draft_test");
  const id = payload.run.id;
  const result = page.getByRole("dialog");
  await expect(
    result.locator("summary").filter({ hasText: /^Execution snapshot$/ }),
  ).toBeVisible();
  await expect(result.getByTestId("workflow-run-graph")).toContainText(
    "Unsaved graph B",
  );
  await result.getByRole("button", { name: "Accept", exact: true }).click();
  await expect(
    result.getByText("Completed", { exact: true }).first(),
  ).toBeVisible();
  await result
    .locator(".overflow-y-auto")
    .first()
    .evaluate((el) => {
      el.scrollTop = 0;
    });
  await page.screenshot({
    path: info.outputPath("trial-result-light.png"),
    fullPage: true,
  });
  await result.getByRole("button", { name: "Close", exact: true }).click();
  await expect(json).toHaveValue(/Unsaved graph B/);
  await json.fill(JSON.stringify(graph("Later graph C"), null, 2));
  await page.getByRole("button", { name: "Apply", exact: true }).click();
  const snapshot = await api.workflowRequest(
    `/api/workflow-test-runs/${id}/definition`,
  );
  expect(
    snapshot.definition.nodes.find((n: { key: string }) => n.key === "review")
      .name,
  ).toBe("Unsaved graph B");
  const unchanged = await api.workflowRequest(
    `/api/workflow-templates/${template.id}`,
  );
  expect(unchanged.revision).toBe(template.revision);
  expect(
    unchanged.definition.nodes.find((n: { key: string }) => n.key === "review")
      .name,
  ).toBe("Saved graph A");
  await page.getByRole("button", { name: "Draft trials", exact: true }).click();
  await expect(
    page
      .getByRole("link")
      .filter({ hasText: payload.execution_ref.definition_hash.slice(0, 12) }),
  ).toBeVisible();
  page.once("dialog", (d) => d.accept());
  await page
    .getByRole("link")
    .filter({ hasText: payload.execution_ref.definition_hash.slice(0, 12) })
    .click();
  await expect(page.getByTestId("workflow-run-graph")).toContainText(
    "Unsaved graph B",
  );
  await expect(
    page.locator("summary").filter({ hasText: /^Execution snapshot$/ }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Dark", exact: true }).click();
  await expect(page.locator("html")).toHaveClass(/dark/);
  await page.screenshot({
    path: info.outputPath("trial-detail-dark.png"),
    fullPage: true,
  });
  await expect
    .poll(() =>
      page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
    )
    .toBe(true);

  // Controlled subprocess output goes through the real claim, delivery and
  // receipt endpoints. No ambient agent executable or external service is used.
  const db = new pg.Client(process.env.DATABASE_URL);
  await db.connect();
  try {
    const worker = await api.seedWorkflowAgent();
    await db.query("UPDATE agent_runtime SET metadata=$2 WHERE id=$1", [
      worker.runtime,
      JSON.stringify({ capabilities: caps }),
    ]);
    const agentGraph = {
      schema_version: 1,
      entry_node: "work",
      nodes: [
        {
          key: "work",
          type: "agent",
          name: "Controlled agent",
          instruction: "Emit deterministic analysis",
          routing: { strategy: "explicit", agent_id: worker.agent },
          submission_schema: "analysis",
          next: ["end"],
        },
        { key: "end", type: "end" },
      ],
    };
    const tpl = await api.workflowRequest("/api/workflow-templates", {
      method: "POST",
      body: JSON.stringify({
        key: `controlled-${Date.now()}`,
        name: "Controlled worker",
        definition: agentGraph,
      }),
    });
    const policy = await api.workflowRequest(
      "/api/workflow-test-runs/settings",
    );
    const start = await api.workflowRequest(
      `/api/workflow-templates/${tpl.id}/test-runs`,
      {
        method: "POST",
        body: JSON.stringify({
          schema_version: "1",
          expected_revision: tpl.revision,
          expected_debug_policy_revision: policy.revision,
          base_draft_version_id: tpl.versions[0].id,
          definition: agentGraph,
          input: {
            title: "Controlled agent",
            description: "Isolated execution",
          },
          project_id: null,
          image_attachment_id: null,
          idempotency_key: crypto.randomUUID(),
          execution_acknowledged: true,
        }),
      },
    );
    const request = async (path: string, body: unknown, execution?: string) =>
      fetch(`${process.env.NEXT_PUBLIC_API_URL}${path}`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${api.getToken()}`,
          "X-Workspace-Slug": ws.slug,
          "X-Client-Capabilities": caps.join(","),
          ...(execution ? { "X-Workflow-Execution-Id": execution } : {}),
        },
        body: JSON.stringify(body),
      });
    await db.query("UPDATE workspace SET repos=$2 WHERE id=$1", [
      ws.id,
      JSON.stringify([
        { url: "https://example.invalid/later-added.git", ref: "main" },
      ]),
    ]);
    const ordinary = await api.workflowDaemonRequest(
      `/api/daemon/runtimes/${worker.runtime}/tasks/claim`,
    );
    expect(ordinary.task).toBeNull();
    await db.query("UPDATE agent_runtime SET metadata=$2 WHERE id=$1", [
      worker.runtime,
      JSON.stringify({ capabilities: [] }),
    ]);
    const incompatible = await request(
      `/api/daemon/runtimes/${worker.runtime}/workflow-test-tasks/claim`,
      { daemon_incarnation_id: crypto.randomUUID() },
    );
    expect(incompatible.status).toBe(503);
    await db.query("UPDATE agent_runtime SET metadata=$2 WHERE id=$1", [
      worker.runtime,
      JSON.stringify({ capabilities: caps }),
    ]);
    const claimResponse = await request(
      `/api/daemon/runtimes/${worker.runtime}/workflow-test-tasks/claim`,
      { daemon_incarnation_id: crypto.randomUUID() },
    );
    expect(claimResponse.status).toBe(200);
    const { task } = await claimResponse.json();
    expect(task.workflow_execution_mode).toBe("draft_test");
    expect(task.issue_id).toBeFalsy();
    expect(task.repos ?? []).toEqual([]);
    const send = async (action: string, body: unknown) => {
      const res = await request(
        `/api/daemon/tasks/${task.id}/${action}`,
        body,
        task.workflow_execution_id,
      );
      expect(res.status).toBe(200);
      return res.json();
    };
    await send("start", {});
    const submission = {
      schema_version: 1,
      step_instance_id: task.workflow_step_instance_id,
      verdict: "pass",
      artifact: { type: "analysis", summary: "Controlled subprocess exited" },
      rationale: "Deterministic evidence",
      confidence: 1,
    };
    const { stdout } = await promisify(execFile)(
      process.execPath,
      [
        "-e",
        "process.stdout.write(process.argv[1])",
        JSON.stringify(submission),
      ],
      { timeout: 5000 },
    );
    await send("messages", {
      messages: [
        {
          seq: 1,
          type: "text",
          content: "Controlled subprocess exited",
          created_at: new Date().toISOString(),
        },
      ],
    });
    await send("complete", {
      output: `<<<MULTICA_SUBMISSION>>>\n${stdout}\n<<<END_MULTICA_SUBMISSION>>>`,
    });
    const receipt = {
      schema_version: "1",
      receipt_id: crypto.randomUUID(),
      kind: "complete",
      process_stopped: true,
      delivery_drained: true,
      process_stopped_at: new Date().toISOString(),
      final_message_seq: 1,
    };
    await send("execution-receipts", receipt);
    await send("execution-receipts", receipt);
    const late = await send("messages", {
      messages: [{ seq: 2, type: "text", content: "late marker" }],
    });
    expect(late.status).toBe("ignored_delivery_closed");
    const completed = await api.workflowRequest(
      `/api/workflow-test-runs/${start.run.id}`,
    );
    expect(completed.run.status).toBe("completed");
    const counts = await db.query(
      "SELECT (SELECT count(*) FROM workflow_debug_task_execution WHERE run_id=$1 AND delivery_drained_at IS NOT NULL)::int AS receipts,(SELECT count(*) FROM task_message WHERE task_id=$2)::int AS messages",
      [start.run.id, task.id],
    );
    expect(counts.rows[0]).toEqual({ receipts: 1, messages: 1 });
    await writeFile(
      info.outputPath("controlled-execution.json"),
      JSON.stringify(
        {
          runId: start.run.id,
          mode: completed.execution_ref.kind,
          status: completed.run.status,
          receipts: 1,
          messages: 1,
          emptyRepositories: true,
          lateWrite: late.status,
        },
        null,
        2,
      ),
    );
    await db.query(
      "UPDATE workflow_run SET debug_purge_after=now()-interval '1 second' WHERE id=$1",
      [start.run.id],
    );
    await expect
      .poll(
        async () =>
          (await api.workflowRequest(`/api/workflow-test-runs/${start.run.id}`))
            .debug.cleanup_state,
        { timeout: 45000 },
      )
      .toBe("purged");
    for (const action of [
      "start",
      "complete",
      "fail",
      "messages",
      "session",
      "cancel-ack",
      "progress",
      "usage",
      "wait-local-directory",
    ]) {
      const ignored = await send(action, "not a valid payload object");
      expect(ignored.status).toBe("ignored_expired");
    }
    const wrongClaim = await request(
      `/api/daemon/tasks/${task.id}/messages`,
      {},
      crypto.randomUUID(),
    );
    expect(wrongClaim.status).toBe(403);
    const human = await request(
      `/api/workflow-test-runs/${start.run.id}/cancel`,
      {},
    );
    expect(human.status).toBe(410);
    await navigate(`/${ws.slug}/workflow-test-runs/${start.run.id}`);
    await expect(
      page.getByText("Details expired. Only execution metadata remains.", {
        exact: true,
      }),
    ).toBeVisible();
    await page.screenshot({
      path: info.outputPath("trial-expired-dark.png"),
      fullPage: true,
    });
  } finally {
    await db.end();
  }
}
test("web draft trial UI and controlled execution", async ({ page }, info) =>
  exercise(page, info, false));
test("desktop draft trial UI and controlled execution", async ({}, info) => {
  const isolated = info.outputPath("desktop-home");
  await mkdir(join(isolated, ".multica"), { recursive: true });
  await mkdir(join(isolated, "appData"), { recursive: true });
  await writeFile(
    join(isolated, ".multica/desktop_prefs.json"),
    JSON.stringify({ autoStart: false, autoStop: false }),
  );
  const bootstrap = info.outputPath("desktop-bootstrap.cjs");
  await writeFile(
    bootstrap,
    `const os=require('node:os');os.homedir=()=>${JSON.stringify(isolated)};const {app}=require('electron');app.setPath('home',${JSON.stringify(isolated)});app.setPath('appData',${JSON.stringify(join(isolated, "appData"))});require(${JSON.stringify(resolve("apps/desktop/out/main/index.js"))});`,
  );
  const env = Object.fromEntries(
    Object.entries(process.env).filter(
      ([key, value]) =>
        value !== undefined &&
        !key.startsWith("MULTICA_") &&
        !["ELECTRON_RUN_AS_NODE", "ELECTRON_RENDERER_URL"].includes(key),
    ),
  ) as Record<string, string>;
  const app = await electron.launch({
    executablePath: resolve(
      "apps/desktop/node_modules/electron/dist/Electron.app/Contents/MacOS/Electron",
    ),
    args: [bootstrap],
    env: { ...env, DESKTOP_APP_SUFFIX: `TES7P2D-${Date.now()}` },
  });
  try {
    const page = await app.firstWindow();
    await page.waitForLoadState("domcontentloaded");
    await exercise(page, info, true);
  } finally {
    await app.evaluate(({ app }) => app.exit(0)).catch(() => {});
    await app.close();
  }
});
