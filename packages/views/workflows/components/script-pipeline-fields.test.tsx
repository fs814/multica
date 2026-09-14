// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { useState } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import { scriptPipelineConfig } from "@multica/core/workflows";
import enWorkflows from "../../locales/en/workflows.json";
import { ScriptPipelineFields } from "./script-pipeline-fields";

afterEach(cleanup);
function Form() {
 const [value, setValue] = useState(scriptPipelineConfig());
 return <I18nProvider locale="en" resources={{ en: { workflows: enWorkflows } }}><ScriptPipelineFields value={value} onChange={setValue} /><output data-testid="value">{JSON.stringify(value)}</output></I18nProvider>;
}
describe("directory scripts form", () => {
 it("edits a path and selects only run, then restores canonical order", async () => {
  render(<Form />);
  const user = userEvent.setup();
  await user.type(screen.getByLabelText(enWorkflows.scripts.directory), "D:/winbuild/project");
  await user.click(screen.getByRole("checkbox", { name: "clone" }));
  await user.click(screen.getByRole("checkbox", { name: "build" }));
  let value = JSON.parse(screen.getByTestId("value").textContent!);
  expect(value.directory).toBe("D:/winbuild/project");
  expect(value.steps).toEqual(["run"]);
  expect(screen.getAllByPlaceholderText(enWorkflows.scripts.auto_script)).toHaveLength(1);
  await user.click(screen.getByRole("checkbox", { name: "clone" }));
  value = JSON.parse(screen.getByTestId("value").textContent!);
  expect(value.steps).toEqual(["clone", "run"]);
 });
 it("requires at least one selected step", async () => {
  render(<Form />);
  const user = userEvent.setup();
  for (const name of ["clone", "build", "run"]) await user.click(screen.getByRole("checkbox", { name }));
  expect(screen.getByRole("alert").textContent).toBe(enWorkflows.scripts.select_step);
 });
});
