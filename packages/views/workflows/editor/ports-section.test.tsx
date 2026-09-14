// @vitest-environment jsdom
import { it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import { WorkflowDefinitionSchema } from "@multica/core/workflows";
import en from "../../locales/en/workflows.json";
import { PortsSection } from "./ports-section";
it("keeps typing local until rename confirmation and prevents duplicate IDs", () => {
  const definition = WorkflowDefinitionSchema.parse({ schema_version: 2, nodes: [{ key: "join", type: "join", input_ports: [{ id: "old", type: "string" }, { id: "occupied", type: "string" }] }] });
  const change = vi.fn(), rename = vi.fn();
  render(<I18nProvider locale="en" resources={{ en: { workflows: en } }}><PortsSection node={definition.nodes[0]!} definition={definition} readOnly={false} onChange={change} onRenamePort={rename} /></I18nProvider>);
  const input = screen.getAllByRole("textbox", { name: "Port ID" })[0]!;
  fireEvent.change(input, { target: { value: "new" } });
  expect(change).not.toHaveBeenCalled(); expect(rename).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Confirm rename" }));
  expect(rename).toHaveBeenCalledWith("input_ports", "old", "new");
  fireEvent.change(input, { target: { value: "occupied" } });
  expect(screen.getByRole("button", { name: "Confirm rename" })).toBeDisabled();
  expect(screen.getByText(/Waits for all activated/)).toBeInTheDocument();
});

it("explains why produced output IDs and coupled inputs cannot be renamed", () => {
  const definition = WorkflowDefinitionSchema.parse({
    schema_version: 2,
    nodes: [
      {
        key: "join",
        type: "join",
        input_ports: [{ id: "value", type: "string" }],
        output_ports: [{ id: "value", type: "string" }],
      },
    ],
  });
  const rename = vi.fn();
  render(
    <I18nProvider locale="en" resources={{ en: { workflows: en } }}>
      <PortsSection
        node={definition.nodes[0]!}
        definition={definition}
        readOnly={false}
        onChange={vi.fn()}
        onRenamePort={rename}
      />
    </I18nProvider>,
  );
  for (const field of screen.getAllByRole("textbox", { name: "Port ID" }))
    expect(field).toBeDisabled();
  expect(screen.getAllByText(/part of the output value contract/)).toHaveLength(
    2,
  );
  expect(rename).not.toHaveBeenCalled();
});
