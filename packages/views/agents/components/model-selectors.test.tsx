import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { runtimeModelsKeys } from "@multica/core/runtimes";
import { renderWithI18n } from "../../test/i18n";
import { ModelDropdown } from "./model-dropdown";
import { ModelPicker } from "./inspector/model-picker";

const apiMocks = vi.hoisted(() => ({
  initiateListModels: vi.fn(),
  getListModelsResult: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api: apiMocks }));

const models = [{ id: "model-a", label: "Model A" }];
const clients: QueryClient[] = [];

beforeEach(() => {
  vi.clearAllMocks();
  apiMocks.initiateListModels.mockResolvedValue({
    id: "request-1", runtime_id: "runtime-1", status: "completed",
    supported: true, models,
  });
});
afterEach(() => {
  cleanup();
  clients.forEach((client) => client.clear());
  clients.length = 0;
});

for (const variant of ["settings", "create"] as const) {
  describe(`${variant} model selection`, () => {
    function setup({ online = false, cached = false } = {}) {
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      clients.push(client);
      if (cached) {
        client.setQueryData(runtimeModelsKeys.forRuntime("runtime-1"), {
          models, supported: true, cached: false,
        });
      }
      const onChange = vi.fn();
      const renderPicker = (runtimeId: string, runtimeOnline: boolean) => (
        <QueryClientProvider client={client}>
          {variant === "settings" ? (
            <ModelPicker runtimeId={runtimeId} runtimeOnline={runtimeOnline}
              value="current-model" onChange={onChange} variant="field" />
          ) : (
            <ModelDropdown runtimeId={runtimeId} runtimeOnline={runtimeOnline}
              value="current-model" onChange={onChange} />
          )}
        </QueryClientProvider>
      );
      const view = renderWithI18n(renderPicker("runtime-1", online));
      fireEvent.click(screen.getByRole("button", { name: /current-model/ }));
      return { onChange, view, renderPicker };
    }

    it("keeps cached models selectable offline without starting discovery", async () => {
      const { onChange } = setup({ cached: true });
      fireEvent.click(await screen.findByRole("button", { name: /Model A/ }));
      expect(onChange).toHaveBeenCalledExactlyOnceWith("model-a");
      expect(apiMocks.initiateListModels).not.toHaveBeenCalled();
    });

    it("explains an offline empty catalog and allows manual model entry", async () => {
      const { onChange } = setup();
      expect(await screen.findByText("Runtime offline — enter manually")).toBeVisible();
      fireEvent.change(screen.getByPlaceholderText("Search or type a model ID"), {
        target: { value: "custom-model" },
      });
      fireEvent.click(await screen.findByRole("button", { name: 'Use "custom-model"' }));
      expect(onChange).toHaveBeenCalledExactlyOnceWith("custom-model");
      expect(apiMocks.initiateListModels).not.toHaveBeenCalled();
    });

    it("explains discovery failure and leaves manual entry usable", async () => {
      apiMocks.initiateListModels.mockRejectedValue(new Error("discovery unavailable"));
      const { onChange } = setup({ online: true });
      expect(await screen.findByText("discovery failed")).toBeVisible();
      fireEvent.change(screen.getByPlaceholderText("Search or type a model ID"), {
        target: { value: "custom-model" },
      });
      fireEvent.click(await screen.findByRole("button", { name: 'Use "custom-model"' }));
      expect(onChange).toHaveBeenCalledExactlyOnceWith("custom-model");
    });

    it("discovers and selects models again when the runtime reconnects", async () => {
      const { onChange, view, renderPicker } = setup();
      view.rerender(renderPicker("runtime-1", true));
      fireEvent.click(await screen.findByRole("button", { name: /Model A/ }));
      expect(onChange).toHaveBeenCalledExactlyOnceWith("model-a");
      expect(apiMocks.initiateListModels).toHaveBeenCalledExactlyOnceWith("runtime-1");
    });

    it("does not reuse another runtime's catalog when switching offline runtimes", async () => {
      const { view, renderPicker } = setup({ cached: true });
      view.rerender(renderPicker("runtime-2", false));
      expect(screen.queryByRole("button", { name: /Model A/ })).not.toBeInTheDocument();
      expect(apiMocks.initiateListModels).not.toHaveBeenCalled();
    });
  });
}
