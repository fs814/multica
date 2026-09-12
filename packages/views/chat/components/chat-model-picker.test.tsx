/**
 * @vitest-environment jsdom
 */
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { ChatModelPicker } from "./chat-model-picker";

// The picker's whole job is a gate: decide whether the composer chip should
// exist, and which runtime's catalog it reads. ModelPicker itself is covered by
// the agents-inspector tests, so it is stubbed to a marker that echoes the
// runtimeId it was handed — that id IS the thing this component must get right.
vi.mock("../../agents/components/inspector/model-picker", () => ({
  ModelPicker: (props: { runtimeId: string; value: string; canEdit?: boolean }) => (
    <div
      data-testid="model-picker"
      data-runtime-id={props.runtimeId}
      data-value={props.value}
      data-can-edit={String(props.canEdit)}
    />
  ),
}));

const presence = vi.hoisted(() => ({
  value: { availability: "online" } as { availability: string } | "loading",
}));
vi.mock("@multica/core/agents", () => ({
  useAgentPresenceDetail: () => presence.value,
}));

const agent = { id: "agent-1", runtime_id: "runtime-knot" };

describe("ChatModelPicker", () => {
  it("renders the picker for a bound, online runtime", () => {
    presence.value = { availability: "online" };
    render(
      <ChatModelPicker
        wsId="ws-1"
        agent={agent}
        value="claude-4.8-opus"
        onChange={vi.fn()}
      />,
    );

    const picker = screen.getByTestId("model-picker");
    expect(picker.dataset.value).toBe("claude-4.8-opus");
    expect(picker.dataset.canEdit).toBe("true");
  });

  // The bug this guards: the catalog must come from the SESSION's agent. The
  // controller's `activeAgent` falls back to availableAgents[0] when no session
  // is selected, so wiring this component to that value showed an unrelated
  // runtime's models — e.g. Claude Code's list for a knot conversation.
  it("resolves the catalog from the agent it is given, not a fallback", () => {
    presence.value = { availability: "online" };
    render(
      <ChatModelPicker
        wsId="ws-1"
        agent={{ id: "agent-knot", runtime_id: "runtime-knot" }}
        value=""
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByTestId("model-picker").dataset.runtimeId).toBe("runtime-knot");
  });

  it("forwards `disabled` as read-only rather than hiding the chip", () => {
    presence.value = { availability: "online" };
    render(
      <ChatModelPicker
        wsId="ws-1"
        agent={agent}
        value="claude-4.8-opus"
        disabled
        onChange={vi.fn()}
      />,
    );

    expect(screen.getByTestId("model-picker").dataset.canEdit).toBe("false");
  });

  it("renders nothing without an agent", () => {
    presence.value = { availability: "online" };
    render(<ChatModelPicker wsId="ws-1" agent={null} value="" onChange={vi.fn()} />);

    expect(screen.queryByTestId("model-picker")).toBeNull();
  });

  it("renders nothing when the agent has no bound runtime", () => {
    presence.value = { availability: "online" };
    for (const runtime_id of [undefined, null, "", "   "]) {
      const { unmount } = render(
        <ChatModelPicker
          wsId="ws-1"
          agent={{ id: "agent-1", runtime_id }}
          value=""
          onChange={vi.fn()}
        />,
      );
      expect(screen.queryByTestId("model-picker")).toBeNull();
      unmount();
    }
  });

  // Model discovery is a round trip to the user's machine, so anything short of
  // `online` has no catalog to offer. The composer already explains those states
  // via OfflineBanner / RuntimeRequiredBanner.
  it("renders nothing unless the runtime is online", () => {
    for (const availability of ["offline", "unstable", "archived"]) {
      presence.value = { availability };
      const { unmount } = render(
        <ChatModelPicker wsId="ws-1" agent={agent} value="" onChange={vi.fn()} />,
      );
      expect(screen.queryByTestId("model-picker")).toBeNull();
      unmount();
    }
  });

  it("renders nothing while presence is still loading", () => {
    presence.value = "loading";
    render(<ChatModelPicker wsId="ws-1" agent={agent} value="" onChange={vi.fn()} />);

    expect(screen.queryByTestId("model-picker")).toBeNull();
  });
});
