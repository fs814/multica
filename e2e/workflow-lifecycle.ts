import { chromium, expect, type Page, type TestInfo } from "@playwright/test";
import { TestApiClient } from "./fixtures";

/** No process runs this agent: claims and outputs are supplied through daemon HTTP. */
export async function verifyWorkflowLifecycle(
  page: Page,
  info: TestInfo,
  api: TestApiClient,
  slug: string,
  navigate: (path: string) => Promise<unknown>,
) {
  const { agent, runtime } = await api.seedWorkflowAgent();
  const graph = {
    schema_version: 1,
    entry_node: "input",
    nodes: [
      {
        key: "input",
        type: "input",
        name: "Request",
        next: ["work"],
        input_fields: [
          { key: "title", type: "text", label: "Title", required: true },
          {
            key: "description",
            type: "textarea",
            label: "Description",
            required: true,
          },
          { key: "details", type: "textarea", label: "Details" },
        ],
      },
      {
        key: "work",
        type: "agent",
        name: "Controlled work",
        instruction: "Produce a deterministic analysis.",
        routing: { strategy: "explicit", agent_id: agent },
        submission_schema: "analysis",
        next: ["review"],
        max_attempts: 3,
      },
      {
        key: "review",
        type: "acceptance",
        name: "Human review",
        acceptance_criteria: ["Check the controlled evidence"],
        rework_targets: ["work"],
        next: ["end"],
      },
      { key: "end", type: "end", name: "Done" },
    ],
  };
  const tpl = await api.workflowRequest("/api/workflow-templates", {
    method: "POST",
    body: JSON.stringify({
      key: `lifecycle-${Date.now()}`,
      name: "Controlled lifecycle",
      definition: graph,
    }),
  });
  const published = await api.workflowRequest(
    `/api/workflow-templates/${tpl.id}/publish`,
    {
      method: "POST",
      body: JSON.stringify({
        revision: tpl.revision,
        draft_version_id: tpl.versions[0].id,
      }),
    },
  );
  const version = published.versions.find(
    (v: { status: string }) => v.status === "published",
  );
  const instance = await api.workflowRequest(
    `/api/workflow-templates/${tpl.id}/input-instances`,
    {
      method: "POST",
      body: JSON.stringify({
        name: "Controlled saved inputs",
        template_version_id: version.id,
        input: {
          title: "Long workflow title " + "release evidence ".repeat(10),
          description: "Controlled test request",
          details: JSON.stringify({
            notes: "Long JSON evidence ".repeat(200),
            nested: { expected: "preserved" },
          }),
        },
      }),
    },
  );
  const instancePath = `/${slug}/workflow-instances/${instance.id}`;
  const start = async () => {
    await navigate(instancePath);
    await page
      .getByRole("button", { name: "Run saved inputs", exact: true })
      .click();
    await expect(page.getByTestId("workflow-run-graph")).toBeVisible();
    const list = await api.workflowRequest(
      `/api/workflow-instances/${instance.id}/runs`,
    );
    return list.runs[0];
  };
  const claim = async (runId: string) => {
    const { task } = await api.workflowDaemonRequest(
      `/api/daemon/runtimes/${runtime}/tasks/claim`,
    );
    expect(task?.workflow_run_id).toBe(runId);
    expect(task.workflow_prompt).toContain("<<<MULTICA_SUBMISSION>>>");
    await api.workflowDaemonRequest(`/api/daemon/tasks/${task.id}/start`);
    return task;
  };
  const complete = async (task: {
    id: string;
    workflow_step_instance_id: string;
  }) => {
    const payload = {
      schema_version: 1,
      step_instance_id: task.workflow_step_instance_id,
      verdict: "pass",
      artifact: {
        type: "analysis",
        summary: "Controlled evidence is complete",
      },
      rationale: "Deterministic test output",
      confidence: 1,
    };
    await api.workflowDaemonRequest(`/api/daemon/tasks/${task.id}/complete`, {
      output: `<<<MULTICA_SUBMISSION>>>\n${JSON.stringify(payload)}\n<<<END_MULTICA_SUBMISSION>>>`,
    });
  };

  const peerBrowser = await chromium.launch();
  const peerContext = await peerBrowser.newContext();
  await peerContext.addInitScript((token) => {
    localStorage.setItem("multica_token", token!);
    localStorage.setItem("multica:chat:isOpen", "false");
    localStorage.setItem("i18nextLng", "en");
  }, api.getToken());
  const peer = await peerContext.newPage();
  const peerRow = peer
    .getByRole("article")
    .filter({ hasText: "Controlled saved inputs" });
  try {
    const cancelled = await start();
    await peer.goto(
      `${process.env.FRONTEND_ORIGIN}/${slug}/workflow-instances?search=Controlled`,
    );
    await expect(peerRow).toContainText("Running");
    const cancelledTask = await claim(cancelled.id);
    await page.getByRole("button", { name: "Cancel run", exact: true }).focus();
    await page.keyboard.press("Enter");
    await expect(
      page.getByText("Cancelled", { exact: true }).first(),
    ).toBeVisible();
    await api.workflowDaemonRequest(
      `/api/daemon/tasks/${cancelledTask.id}/cancel-ack`,
    );
    await expect(peerRow).toContainText("Cancelled", { timeout: 8_000 });
    expect(
      (await api.workflowRequest(`/api/workflow-runs/${cancelled.id}`)).status,
    ).toBe("cancelled");
    await page.screenshot({
      path: info.outputPath("lifecycle-cancelled.png"),
      fullPage: true,
    });

    const reviewed = await start();
    await expect(peerRow).toContainText("Running", { timeout: 8_000 });
    let task = await claim(reviewed.id);
    // Keep an instance list visible while another client completes the agent task.
    await navigate(`/${slug}/workflow-instances?search=Controlled`);
    const row = page
      .getByRole("article")
      .filter({ hasText: "Controlled saved inputs" });
    await expect(row).toContainText("Running");
    await peerContext.setOffline(true);
    await complete(task);
    // Less than the 15-second polling fallback: proves realtime invalidation/refetch.
    await expect(row).toContainText("Waiting for review", { timeout: 8_000 });
    await peerContext.setOffline(false);
    await expect(peerRow).toContainText("Waiting for review", {
      timeout: 8_000,
    });
    await navigate(`/${slug}/workflow-runs/${reviewed.id}`);
    await expect(
      page.getByRole("button", { name: "Accept", exact: true }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Request rework", exact: true })
      .click();
    await page
      .getByPlaceholder("What is wrong, and what would make it right")
      .fill("Add the missing controlled detail");
    await page
      .getByRole("combobox", { name: "Send the work back to", exact: true })
      .click();
    await page.getByRole("option", { name: "work", exact: true }).click();
    await page
      .getByRole("button", { name: "Request rework", exact: true })
      .click();
    await expect
      .poll(
        async () =>
          (await api.workflowRequest(`/api/workflow-runs/${reviewed.id}`))
            .status,
      )
      .toBe("running");
    await expect(peerRow).toContainText("Running", { timeout: 8_000 });
    task = await claim(reviewed.id);
    await complete(task);
    await expect(
      page.getByRole("button", { name: "Accept", exact: true }),
    ).toBeVisible({ timeout: 8_000 });
    await page.screenshot({
      path: info.outputPath("lifecycle-review.png"),
      fullPage: true,
    });
    await page.getByRole("button", { name: "Accept", exact: true }).focus();
    await page.keyboard.press("Enter");
    await expect
      .poll(
        async () =>
          (await api.workflowRequest(`/api/workflow-runs/${reviewed.id}`))
            .status,
      )
      .toBe("completed");
    await expect(peerRow).toContainText("Completed", { timeout: 8_000 });
    await navigate(instancePath);
    await expect(
      page.getByText("Completed", { exact: true }).first(),
    ).toBeVisible();
    await navigate(`/${slug}/workflow-runs/${reviewed.id}`);
    const run = await api.workflowRequest(`/api/workflow-runs/${reviewed.id}`);
    expect(
      run.steps.filter((s: { node_key: string }) => s.node_key === "work"),
    ).toHaveLength(2);
    await page.screenshot({
      path: info.outputPath("lifecycle-completed-graph.png"),
      fullPage: true,
    });
    const details = page.getByText(run.input.details, { exact: true });
    await details.scrollIntoViewIfNeeded();
    await expect(details).toBeInViewport();
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
    await page.screenshot({
      path: info.outputPath("lifecycle-completed-long-input.png"),
      fullPage: true,
    });
  } finally {
    await peerBrowser.close();
  }
}
