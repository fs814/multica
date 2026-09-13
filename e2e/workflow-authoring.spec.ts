import {
  test,
  expect,
  _electron as electron,
  type Page,
  type TestInfo,
} from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve, join } from "node:path";

const graph = () => ({
  schema_version: 2,
  entry_node: "input",
  nodes: [
    {
      key: "input",
      type: "input",
      name: "Request",
      next: ["gate"],
      next_ids: ["ig"],
      output_ports: [
        { id: "region", type: "string" },
        { id: "count", type: "number" },
      ],
      input_fields: [
        { key: "title", label: "Title", type: "text", required: true },
        { key: "region", label: "Region", type: "text", required: true },
        { key: "legacy", label: "Legacy", type: "text" },
        { key: "obsolete", label: "Obsolete", type: "text" },
      ],
    },
    {
      key: "gate",
      type: "condition",
      name: "Region choice",
      input_ports: [{ id: "choice", type: "string", required: true }],
      branches: [
        {
          id: "west",
          target: "left",
          predicate: { input_port: "choice", equals: "west" },
        },
        { id: "default", target: "right" },
      ],
    },
    {
      key: "left",
      type: "join",
      name: "West branch",
      next: ["merge"],
      next_ids: ["lm"],
    },
    {
      key: "right",
      type: "join",
      name: "Default branch",
      next: ["merge"],
      next_ids: ["rm"],
    },
    {
      key: "merge",
      type: "join",
      name: "Active predecessors",
      next: ["end"],
      next_ids: ["me"],
    },
    { key: "end", type: "end", name: "Finished" },
  ],
  data_edges: [
    {
      id: "binding",
      source: "input",
      source_port: "region",
      target: "gate",
      target_port: "choice",
      order: 0,
    },
  ],
});

async function exercise(
  page: Page,
  info: TestInfo,
  desktop: boolean,
  width: number,
) {
  test.setTimeout(180000);
  page.setDefaultTimeout(20000);
  await page.setViewportSize({ width, height: width === 1280 ? 800 : 900 });
  const api = new TestApiClient();
  await api.login(`authoring-${Date.now()}@example.com`, "Authoring tester");
  const ws = await api.ensureWorkspace(
    "Authoring verification",
    `authoring-${Date.now()}`,
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
  const tpl = await api.workflowRequest("/api/workflow-templates", {
    method: "POST",
    body: JSON.stringify({
      key: `author-${Date.now()}`,
      name: "Authoring integration",
      definition: graph(),
    }),
  });
  const publish = async () => {
    const draft = await api.workflowRequest(
      `/api/workflow-templates/${tpl.id}?definition=draft`,
    );
    return api.workflowRequest(`/api/workflow-templates/${tpl.id}/publish`, {
      method: "POST",
      body: JSON.stringify({
        revision: draft.revision,
        draft_version_id: draft.versions.find(
          (v: { status: string }) => v.status === "draft",
        ).id,
      }),
    });
  };
  const first = await publish();
  const v1 = first.versions.find(
    (v: { status: string }) => v.status === "published",
  ).id;
  const instance = await api.workflowRequest(
    `/api/workflow-templates/${tpl.id}/input-instances`,
    {
      method: "POST",
      body: JSON.stringify({
        name: "Retained input",
        input: {
          title: "Branch run",
          description: "P2 isolation",
          region: "west",
          legacy: "preserve this",
          obsolete: "remove this",
        },
        template_version_id: v1,
        project_id: null,
        idempotency_key: crypto.randomUUID(),
      }),
    },
  );
  const historical = await api.workflowRequest(
    `/api/workflow-instances/${instance.id}/run`,
    {
      method: "POST",
      body: JSON.stringify({
        mode: "saved",
        revision: instance.revision,
        idempotency_key: crypto.randomUUID(),
      }),
    },
  );
  await navigate(`/${ws.slug}/workflows/${tpl.id}`);
  await page.locator('.react-flow__node[data-id="gate"]').click();
  const panel = page.getByRole("complementary", { name: "Step properties" });
  await panel.getByRole("button", { name: "Disconnect binding" }).click();
  await page.getByRole("button", { name: "Validate", exact: true }).click();
  const diagnostic = page.getByRole("button", {
    name: /gate\/choice: required input has no source/,
  });
  await expect(diagnostic).toBeVisible();
  await diagnostic.click();
  await expect(page.locator('.react-flow__node[data-id="gate"]')).toHaveClass(
    /selected/,
  );
  await page.screenshot({
    path: info.outputPath("diagnostic-focus.png"),
    fullPage: true,
  });
  await panel.getByRole("combobox", { name: "Source output: choice" }).click();
  await expect(
    page.getByRole("option", { name: "Request / count · number" }),
  ).toBeDisabled();
  await page.getByRole("option", { name: "Request / region · string" }).click();
  const id = panel.getByRole("textbox", { name: "Port ID" }).first();
  await id.fill("location");
  await expect(panel.getByText(/input \/ region → choice/)).toBeVisible();
  await panel.getByRole("button", { name: "Confirm rename" }).click();
  await expect(panel.getByText(/input \/ region → location/)).toBeVisible();
  await page.getByRole("button", { name: "Undo", exact: true }).click();
  await expect(
    panel.getByRole("textbox", { name: "Port ID" }).first(),
  ).toHaveValue("choice");
  await page.getByRole("button", { name: "Redo", exact: true }).click();
  await page.getByRole("button", { name: "Validate", exact: true }).click();
  await expect(page.getByText("No problems found.")).toBeVisible();
  await page.screenshot({
    path: info.outputPath("typed-binding.png"),
    fullPage: true,
  });
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Save", exact: true }),
  ).toBeDisabled();
  const saved = await api.workflowRequest(
    `/api/workflow-templates/${tpl.id}?definition=draft`,
  );
  expect(
    saved.definition.nodes.find((n: { key: string }) => n.key === "gate")
      .branches[0].predicate.input_port,
  ).toBe("location");
  expect(saved.definition.data_edges[0].target_port).toBe("location");
  expect(saved.definition.schema_version).toBe(2);
  const updated = saved.definition;
  updated.nodes[0].input_fields = [
    { key: "title", label: "Title", type: "text", required: true },
    {
      key: "region",
      label: "Region",
      type: "select",
      required: true,
      options: ["west", "east"],
    },
    { key: "new_field", label: "Additional context", type: "textarea" },
  ];
  await api.workflowRequest(`/api/workflow-templates/${tpl.id}`, {
    method: "PATCH",
    body: JSON.stringify({ revision: saved.revision, definition: updated }),
  });
  const second = await publish();
  const v2 = second.versions.find(
    (v: { version: number; status: string }) =>
      v.version === 2 && v.status === "published",
  ).id;
  await page.reload();
  await page.getByRole("button", { name: "Compare versions" }).click();
  const compare = page.getByRole("region", { name: "Compare versions" });
  await compare.getByRole("combobox", { name: "Earlier version" }).click();
  await page.getByRole("option", { name: "v1", exact: true }).click();
  await expect(compare.getByTestId("input-version-diff")).toContainText(
    "Type changed",
  );
  await expect(compare.getByTestId("input-version-diff")).toContainText(
    "Removed",
  );
  await expect(compare.getByTestId("input-version-diff")).toContainText(
    "Added",
  );
  await expect(page.getByRole("listbox")).not.toBeVisible();
  await page.screenshot({
    path: info.outputPath("version-comparison.png"),
    fullPage: true,
  });
  await navigate(`/${ws.slug}/workflow-instances/${instance.id}`);
  await page
    .getByRole("button", { name: "Review latest published version" })
    .click();
  const apply = page.getByRole("button", {
    name: "Apply version to this edit",
  });
  await expect(apply).toBeDisabled();
  await page
    .getByRole("group", { name: /legacy/ })
    .getByRole("radio", { name: "Keep value" })
    .check();
  await page
    .getByRole("group", { name: /obsolete/ })
    .getByRole("radio", { name: "Remove", exact: true })
    .check();
  await expect(apply).toBeEnabled();
  await apply.scrollIntoViewIfNeeded();
  await page.screenshot({
    path: info.outputPath("upgrade-decisions.png"),
    fullPage: true,
  });
  await apply.click();
  expect(
    (await api.workflowRequest(`/api/workflow-instances/${instance.id}`))
      .template_version_id,
  ).toBe(v1);
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect
    .poll(
      async () =>
        (await api.workflowRequest(`/api/workflow-instances/${instance.id}`))
          .template_version_id,
    )
    .toBe(v2);
  const current = await api.workflowRequest(
    `/api/workflow-instances/${instance.id}`,
  );
  expect(current.input.legacy).toBe("preserve this");
  expect(current.input).not.toHaveProperty("obsolete");
  await expect(
    page.getByRole("button", { name: "Run saved inputs" }),
  ).toBeDisabled();
  const legacy = page
    .getByText(
      "Field legacy is not in this version. Keep it for review or explicitly remove it before running.",
      { exact: true },
    )
    .locator("..");
  page.once("dialog", (dialog) => dialog.accept());
  await legacy.getByRole("button", { name: "Remove", exact: true }).click();
  await page.getByRole("button", { name: "Save changes", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Run saved inputs" }),
  ).toBeEnabled();
  await page.getByRole("button", { name: "Run saved inputs" }).click();
  await expect(page.getByTestId("workflow-run-graph")).toContainText(
    "Active predecessors",
  );
  await expect(
    page.getByText("Completed", { exact: true }).first(),
  ).toBeVisible();
  const old = await api.workflowRequest(`/api/workflow-runs/${historical.id}`);
  expect(old.template_version_id).toBe(v1);
  expect(old.input.obsolete).toBe("remove this");
  await page.getByRole("button", { name: "Dark", exact: true }).click();
  await expect(page.locator("html")).toHaveClass(/dark/);
  await expect(page.getByTestId("workflow-run-graph")).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
    )
    .toBe(true);
  await page.screenshot({
    path: info.outputPath("upgraded-run-dark.png"),
    fullPage: true,
  });
}

for (const width of [1280, 1440]) {
  test(`web P2 authoring ${width}`, async ({ page }, info) =>
    exercise(page, info, false, width));
  test(`desktop P2 authoring ${width}`, async ({}, info) => {
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
      env: { ...env, DESKTOP_APP_SUFFIX: `TES7P2-${Date.now()}` },
    });
    try {
      const page = await app.firstWindow();
      await page.waitForLoadState("domcontentloaded");
      await exercise(page, info, true, width);
    } finally {
      await app.evaluate(({ app }) => app.exit(0)).catch(() => {});
      await app.close();
    }
  });
}
