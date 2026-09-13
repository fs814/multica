import {
  test,
  expect as baseExpect,
  _electron as electron,
  type Page,
  type TestInfo,
} from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { verifyWorkflowLifecycle } from "./workflow-lifecycle";

import { mkdir, writeFile } from "node:fs/promises";
import { resolve, join } from "node:path";

const expect = baseExpect.configure({ timeout: 30000 });

const definition = (name: string) => ({
  schema_version: 2,
  entry_node: "input",
  nodes: [
    {
      key: "input",
      type: "input",
      name: "Request",
      next: ["end"],
      next_ids: ["finish"],
      input_fields: [
        { key: "title", label: "Title", type: "text", required: true },
        { key: "region", label: "Region", type: "text", required: true },
      ],
    },
    { key: "end", type: "end", name },
  ],
});

async function exerciseWorkflow(
  page: Page,
  testInfo: TestInfo,
  viewport: { width: number; height: number },
  desktop = false,
) {
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
  test.setTimeout(180_000);
  page.setDefaultTimeout(20_000);
  await page.setViewportSize(viewport);
  const api = new TestApiClient();
  await api.login(
    `workflow-${Date.now()}-${viewport.width}@example.com`,
    "Workflow tester",
  );
  const ws = await api.ensureWorkspace(
    "Workflow verification",
    `workflow-${Date.now()}`,
  );
  await api.markUserOnboarded();
  const loginScript = (token: string | null) => {
    localStorage.setItem("multica_token", token!);
    localStorage.setItem("multica:chat:isOpen", "false");
    localStorage.setItem("i18nextLng", "en");
  };
  await page.addInitScript(loginScript, api.getToken());
  if (desktop) {
    await page.evaluate(loginScript, api.getToken());
    await page.reload();
  }
  const tpl = await api.workflowRequest("/api/workflow-templates", {
    method: "POST",
    body: JSON.stringify({
      key: `path-${Date.now()}`,
      name: "Release checks",
      definition: definition("Original finish"),
    }),
  });
  await navigate(`/${ws.slug}/workflows`);
  await page.getByText("Release checks", { exact: true }).click();
  await page
    .getByRole("button", { name: "Toggle the JSON view", exact: true })
    .click();
  const json = page.getByRole("textbox", {
    name: /Workflow.*JSON|JSON.*workflow/i,
  });
  await json.fill(JSON.stringify(definition("Approved finish"), null, 2));
  await page.getByRole("button", { name: "Apply", exact: true }).click();
  // Reject a real platform navigation while the graph is dirty.
  page.once("dialog", (dialog) => dialog.dismiss());
  await page
    .getByRole("link", { name: "Workflows", exact: true })
    .first()
    .click();
  await expect(json).toHaveValue(/Approved finish/);
  // Exercise the actual shell/browser entry points, preserving both graph and raw JSON.
  const pendingJson = JSON.stringify(
    definition("Unapplied draft " + "long text ".repeat(80)),
    null,
    2,
  );
  await json.fill(pendingJson);
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
  await json.press("End");
  await json.press("Delete");
  await expect(json).toHaveValue(pendingJson);
  const rejectNavigation = async (action: () => Promise<unknown>) => {
    let count = 0;
    const dismiss = async (dialog: import("@playwright/test").Dialog) => {
      count++;
      await dialog.dismiss();
    };
    page.on("dialog", dismiss);
    try {
      await action();
      await expect.poll(() => count).toBe(1);
      await expect(json).toHaveValue(pendingJson);
      expect(count).toBe(1);
      // Let the cancelled browser traversal settle before another history command.
      await page.waitForTimeout(150);
    } finally {
      page.off("dialog", dismiss);
    }
  };
  if (desktop) {
    await rejectNavigation(() =>
      page.getByRole("button", { name: "Go back", exact: true }).click(),
    );
    await rejectNavigation(async () => {
      await json.blur();
      await page.keyboard.press("Meta+ArrowLeft");
    });
    await rejectNavigation(() =>
      page.evaluate(() =>
        window.dispatchEvent(new MouseEvent("mouseup", { button: 3 })),
      ),
    );
    await page
      .getByRole("button", { name: "Go back", exact: true })
      .click({ button: "right" });
    await rejectNavigation(() =>
      page
        .getByRole("menuitem")
        .filter({ hasText: "Workflows" })
        .first()
        .click(),
    );
    await page
      .getByRole("link", { name: "Workflows", exact: true })
      .first()
      .click({ modifiers: ["Meta"] });
    const keepTab = page
      .getByRole("button", { name: "Workflows", exact: true })
      .last();
    await expect(keepTab).toBeVisible();
    await rejectNavigation(() => keepTab.click());
    await keepTab.click({ button: "right" });
    await rejectNavigation(() =>
      page
        .getByRole("menuitem", { name: "Close other tabs", exact: true })
        .click(),
    );
  } else {
    await rejectNavigation(() => page.evaluate(() => history.back()));
    await rejectNavigation(async () => {
      await json.blur();
      await page.keyboard.press("Meta+[");
    });
  }
  await page
    .getByRole("button", { name: "Workflow instances", exact: true })
    .click();
  if (!desktop) await expect(page).toHaveURL(/section=instances/);
  await page.getByRole("button", { name: "Canvas", exact: true }).click();
  await expect(json).toHaveValue(pendingJson);
  await page.screenshot({
    path: testInfo.outputPath(`dirty-json-${viewport.width}.png`),
    fullPage: true,
  });
  await json.fill(JSON.stringify(definition("Approved finish"), null, 2));
  await page.getByRole("button", { name: "Apply", exact: true }).focus();
  await page.keyboard.press("Enter");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("Draft saved", { exact: true })).toBeVisible();
  let cleanPrompts = 0;
  const cleanDialog = async (dialog: import("@playwright/test").Dialog) => {
    cleanPrompts++;
    await dialog.dismiss();
  };
  page.on("dialog", cleanDialog);
  await page
    .getByRole("button", { name: "Workflow instances", exact: true })
    .click();
  if (!desktop) await expect(page).toHaveURL(/section=instances/);
  if (desktop)
    await page.getByRole("button", { name: "Go back", exact: true }).click();
  else await page.evaluate(() => history.back());
  await expect(json).toBeVisible();
  await page
    .getByRole("link", { name: "Workflows", exact: true })
    .first()
    .click();
  await expect(
    page.getByText("Release checks", { exact: true }).last(),
  ).toBeVisible();
  if (!desktop)
    await expect(page).toHaveURL(new RegExp(`/${ws.slug}/workflows$`));
  if (desktop)
    await page.getByRole("button", { name: "Go back", exact: true }).click();
  else await page.evaluate(() => history.back());
  await expect(
    page.getByRole("button", { name: "Toggle the JSON view", exact: true }),
  ).toBeVisible();
  page.off("dialog", cleanDialog);
  expect(cleanPrompts).toBe(0);
  await page
    .getByRole("button", { name: "Toggle the JSON view", exact: true })
    .click();
  await json.fill(pendingJson);
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
  if (desktop) {
    await rejectNavigation(() =>
      page.getByRole("button", { name: "Go forward", exact: true }).click(),
    );
    await rejectNavigation(async () => {
      await json.blur();
      await page.keyboard.press("Meta+ArrowRight");
    });
    await rejectNavigation(() =>
      page.evaluate(() =>
        window.dispatchEvent(new MouseEvent("mouseup", { button: 4 })),
      ),
    );
    await page
      .getByRole("button", { name: "Go forward", exact: true })
      .click({ button: "right" });
    await rejectNavigation(() =>
      page
        .getByRole("menuitem")
        .filter({ hasText: "Workflows" })
        .first()
        .click(),
    );
  } else {
    await rejectNavigation(() => page.evaluate(() => history.forward()));
    await rejectNavigation(async () => {
      await json.blur();
      await page.keyboard.down("Meta");
      await page.keyboard.press("BracketRight");
      await page.keyboard.up("Meta");
    });
  }
  await json.fill(JSON.stringify(definition("Approved finish"), null, 2));
  await page.getByRole("button", { name: "Apply", exact: true }).click();

  await page.getByRole("button", { name: "Publish", exact: true }).click();
  await expect(
    page.getByText("Workflow published", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Toggle the JSON view", exact: true })
    .first()
    .click();
  await page.screenshot({
    path: testInfo.outputPath(`editor-${viewport.width}.png`),
    fullPage: true,
  });
  await page
    .getByRole("button", { name: "Save as instance", exact: true })
    .click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Instance name").fill("Release inputs");
  await dialog
    .getByRole("textbox", { name: "Title", exact: true })
    .fill("Release 42");
  await dialog
    .getByRole("textbox", { name: "Region", exact: true })
    .fill("APAC");
  await dialog
    .getByRole("textbox", { name: "Description", exact: true })
    .fill("Verify the release in isolation");
  await dialog
    .getByRole("button", { name: "Create instance", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Run saved inputs", exact: true }),
  ).toBeVisible();
  if (!desktop) await expect(page).toHaveURL(/workflow-instances\//);
  const instances = await api.workflowRequest(
    `/api/workflow-instances?template_id=${tpl.id}`,
  );
  const instanceId = instances.instances[0].id;
  await expect(
    page.getByRole("button", { name: "Run saved inputs", exact: true }),
  ).toBeEnabled();
  await page
    .getByRole("button", { name: "Run saved inputs", exact: true })
    .click();
  if (!desktop) await expect(page).toHaveURL(/workflow-runs\//);
  await expect(page.getByText("APAC", { exact: true })).toBeVisible();
  await expect(page.getByTestId("workflow-run-graph")).toContainText(
    "Approved finish",
  );
  const old = await api.workflowRequest(
    `/api/workflow-templates/${tpl.id}?definition=draft`,
  );
  const saved = await api.workflowRequest(`/api/workflow-templates/${tpl.id}`, {
    method: "PATCH",
    body: JSON.stringify({
      revision: old.revision,
      definition: definition("Newer finish"),
    }),
  });
  await api.workflowRequest(`/api/workflow-templates/${tpl.id}/publish`, {
    method: "POST",
    body: JSON.stringify({
      revision: saved.revision,
      draft_version_id: saved.versions.find(
        (v: { status: string }) => v.status === "draft",
      ).id,
    }),
  });
  if (!desktop) await page.reload();
  await expect(page.getByTestId("workflow-run-graph")).toContainText(
    "Approved finish",
  );
  await expect(page.getByTestId("workflow-run-graph")).not.toContainText(
    "Newer finish",
  );
  await page.screenshot({
    path: testInfo.outputPath(`run-${viewport.width}-light.png`),
    fullPage: true,
  });
  await page.getByRole("button", { name: "Dark", exact: true }).click();
  await expect(page.locator("html")).toHaveClass(/dark/);
  await expect(
    page.getByRole("button", { name: "Light", exact: true }),
  ).toBeVisible();
  await page.screenshot({
    path: testInfo.outputPath(`run-${viewport.width}-dark.png`),
    fullPage: true,
  });
  await navigate(
    `/${ws.slug}/workflows/${tpl.id}?section=instances&search=Release`,
  );
  await expect(
    page.getByRole("textbox", { name: "Search instances" }),
  ).toHaveValue("Release");
  await expect(
    page.getByRole("link", { name: "Release inputs", exact: true }),
  ).toBeVisible();
  await navigate(`/${ws.slug}/workflow-instances/${instanceId}`);
  await page.getByRole("textbox", { name: "Region", exact: true }).fill("EMEA");
  await page
    .getByRole("button", { name: "Run these edits only", exact: true })
    .click();
  await expect(page.getByTestId("workflow-run-graph")).toContainText(
    "Approved finish",
  );
  await expect(page.getByText("EMEA", { exact: true })).toBeVisible();
  expect(
    (await api.workflowRequest(`/api/workflow-instances/${instanceId}`)).input
      .region,
  ).toBe("APAC");
  await navigate(`/${ws.slug}/workflow-instances/${instanceId}`);
  await page
    .getByRole("button", { name: "Rerun this snapshot", exact: true })
    .last()
    .click();
  await expect(page.getByText("APAC", { exact: true })).toBeVisible();
  await expect(page.getByTestId("workflow-run-graph")).toContainText(
    "Approved finish",
  );
  if (viewport.width === 1440 && !desktop) {
    const ids = new Set<string>();
    for (let i = 0; i < 31; i++) {
      const run = await api.workflowRequest(
        `/api/workflow-instances/${instanceId}/run`,
        {
          method: "POST",
          body: JSON.stringify({
            mode: "saved",
            revision: 1,
            idempotency_key: `pagination-${i}`,
          }),
        },
      );
      ids.add(run.id);
    }
    expect(ids.size).toBe(31);
    await navigate(`/${ws.slug}/workflows/${tpl.id}?section=runs`);
    await expect(page.getByRole("row")).toHaveCount(31);
    await page.getByRole("button", { name: "Next", exact: true }).click();
    await expect(page).toHaveURL(/run_offset=30/);
    await expect(page.getByRole("row")).toHaveCount(5);
    await page
      .getByRole("combobox", { name: "Status", exact: true })
      .selectOption("completed");
    await expect(page).not.toHaveURL(/run_offset/);
    await expect(page.getByRole("row")).toHaveCount(31);
    await page.getByRole("row").nth(1).click();
    await expect(page.getByTestId("workflow-run-graph")).toBeVisible();
    await page.getByRole("link", { name: "Runs", exact: true }).last().click();
    await expect(page).toHaveURL(/section=runs.*status=completed/);
  }

  await verifyWorkflowLifecycle(page, testInfo, api, ws.slug, navigate);
  await api.cleanup();
}

for (const viewport of [
  { width: 1440, height: 900 },
  { width: 1280, height: 800 },
]) {
  test(`workflow edit → publish → instance → run at ${viewport.width}`, async ({
    page,
  }, testInfo) => {
    await exerciseWorkflow(page, testInfo, viewport);
  });
}

for (const viewport of [
  { width: 1280, height: 800 },
  { width: 1440, height: 900 },
])
  test(`desktop workflow main path ${viewport.width}`, async ({}, testInfo) => {
    test.skip(
      process.env.WORKFLOW_DESKTOP_E2E !== "1",
      "Requires a locally built Electron app",
    );
    test.setTimeout(180_000);
    const isolated = testInfo.outputPath("desktop-home");
    await mkdir(join(isolated, ".multica"), { recursive: true });
    await mkdir(join(isolated, "appData"), { recursive: true });
    await writeFile(
      join(isolated, ".multica/desktop_prefs.json"),
      JSON.stringify({ autoStart: false, autoStop: false }),
    );
    const bootstrap = testInfo.outputPath("desktop-bootstrap.cjs");
    await writeFile(
      bootstrap,
      `const os = require('node:os'); os.homedir = () => ${JSON.stringify(isolated)};
    const { app } = require('electron');
    app.setPath('home', ${JSON.stringify(isolated)});
    app.setPath('appData', ${JSON.stringify(join(isolated, "appData"))});
    require(${JSON.stringify(resolve("apps/desktop/out/main/index.js"))});`,
    );
    const env = Object.fromEntries(
      Object.entries(process.env).filter(
        ([key, value]) =>
          value !== undefined &&
          !key.startsWith("MULTICA_") &&
          key !== "ELECTRON_RUN_AS_NODE" &&
          key !== "ELECTRON_RENDERER_URL",
      ),
    ) as Record<string, string>;
    const app = await electron.launch({
      executablePath: resolve(
        "apps/desktop/node_modules/electron/dist/Electron.app/Contents/MacOS/Electron",
      ),
      args: [bootstrap],
      env: { ...env, DESKTOP_APP_SUFFIX: `TES7-${Date.now()}` },
    });
    const page = await app.firstWindow();
    try {
      await page.waitForLoadState("domcontentloaded");
      await exerciseWorkflow(page, testInfo, viewport, true);
    } catch (error) {
      console.error(error instanceof Error ? error.message : String(error));
      await page.screenshot({
        path: testInfo.outputPath("desktop-failure.png"),
        fullPage: true,
      });
      await writeFile(
        testInfo.outputPath("desktop-failure.txt"),
        await page.locator("body").innerText(),
      );
      throw error;
    } finally {
      // Dirty-page beforeunload prompts must not block test-process cleanup.
      await app.evaluate(({ app }) => app.exit(0)).catch(() => {});
      await app.close();
    }
  });

test("100 node / 150 edge editor response sample", async ({
  page,
}, testInfo) => {
  test.setTimeout(120_000);
  const api = new TestApiClient();
  await api.login(
    `workflow-perf-${Date.now()}@example.com`,
    "Workflow performance",
  );
  const ws = await api.ensureWorkspace(
    "Workflow performance",
    `wf-perf-${Date.now()}`,
  );
  await api.markUserOnboarded();
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.addInitScript(
    (token) => localStorage.setItem("multica_token", token!),
    api.getToken(),
  );
  const nodes = Array.from({ length: 100 }, (_, i) => ({
    key: `n${i}`,
    name: `Node ${i}`,
    type: i === 0 ? "input" : i === 99 ? "end" : "fan_out",
    next:
      i === 99
        ? []
        : i >= 1 && i <= 51
          ? [`n${i + 1}`, `n${i + 2}`]
          : [`n${i + 1}`],
    next_ids:
      i === 99 ? [] : i >= 1 && i <= 51 ? [`e${i}-a`, `e${i}-b`] : [`e${i}`],
  }));
  expect(nodes.reduce((n, node) => n + node.next.length, 0)).toBe(150);
  const tpl = await api.workflowRequest("/api/workflow-templates", {
    method: "POST",
    body: JSON.stringify({
      key: `perf-${Date.now()}`,
      name: "100 node response sample",
      definition: { schema_version: 2, entry_node: "n0", nodes },
    }),
  });
  const started = Date.now();
  await page.goto(`/${ws.slug}/workflows/${tpl.id}`);
  await expect(page.locator(".react-flow__node")).toHaveCount(100);
  const firstScreenMs = Date.now() - started;
  const samples = await page.evaluate(async () => {
    const result: number[] = [];
    for (let i = 1; i <= 40; i++) {
      const id = i * 2;
      const node = document.querySelector(`[data-id="n${id}"]`)!;
      const start = performance.now();
      node.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      await new Promise<void>((resolve, reject) => {
        const check = () => {
          const input = document.querySelector<HTMLInputElement>(
            'input[placeholder="What this step does"]',
          );
          if (input?.value === `Node ${id}`) resolve();
          else if (performance.now() - start > 2000)
            reject(new Error("Selection did not reach properties panel"));
          else requestAnimationFrame(check);
        };
        requestAnimationFrame(check);
      });
      result.push(performance.now() - start);
    }
    return result;
  });
  const sorted = [...samples].sort((a, b) => a - b);
  const report = {
    nodes: 100,
    edges: 150,
    samplesMs: samples,
    p95Ms: sorted[Math.ceil(sorted.length * 0.95) - 1],
    firstScreenMs,
    mode: `Next.js ${process.env.WORKFLOW_WEB_BUILD ?? "development"} build; click dispatch to selected properties visible, sampled at animation frames`,
    viewport: { width: 1440, height: 900 },
  };
  await writeFile(
    testInfo.outputPath("workflow-performance.json"),
    JSON.stringify(report, null, 2),
  );
  await page.screenshot({
    path: testInfo.outputPath("workflow-100-nodes.png"),
    fullPage: true,
  });
});
