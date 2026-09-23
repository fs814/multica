// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  knotRuntimeConfigEquals,
  knotToolTarget,
  parseKnotRuntimeConfig,
  serializeKnotRuntimeConfig,
  withKnotAgentId,
  withKnotClientUuid,
} from "./knot-runtime-config";

const AGENT_ID = "ec4633074fe4413c83218e1f36b8e24d";
const CLIENT_UUID = "68b7d6d7-8eb5-4598-830e-d71bcc739672";

describe("parseKnotRuntimeConfig", () => {
  it("reads agent_id and client_uuid out of the knot block", () => {
    expect(
      parseKnotRuntimeConfig({
        knot: { agent_id: AGENT_ID, client_uuid: "remote" },
      }),
    ).toEqual({ agentId: AGENT_ID, clientUuid: "remote" });
  });

  it("keeps a stored client_uuid even when there is no agent_id", () => {
    expect(
      parseKnotRuntimeConfig({ knot: { client_uuid: CLIENT_UUID } }),
    ).toEqual({ clientUuid: CLIENT_UUID });
  });

  it("collapses a null/blank payload to an empty config", () => {
    expect(parseKnotRuntimeConfig(null)).toEqual({});
    expect(parseKnotRuntimeConfig({ knot: { client_uuid: "  " } })).toEqual({});
  });
});

describe("serializeKnotRuntimeConfig", () => {
  it("emits both fields when set", () => {
    expect(
      serializeKnotRuntimeConfig({ agentId: AGENT_ID, clientUuid: "remote" }),
    ).toEqual({ knot: { agent_id: AGENT_ID, client_uuid: "remote" } });
  });

  it("emits client_uuid alone when no agent id is chosen", () => {
    expect(serializeKnotRuntimeConfig({ clientUuid: CLIENT_UUID })).toEqual({
      knot: { client_uuid: CLIENT_UUID },
    });
  });

  it("drops the knot block entirely when nothing is set", () => {
    expect(serializeKnotRuntimeConfig({})).toEqual({});
    expect(serializeKnotRuntimeConfig({ agentId: "  ", clientUuid: "" })).toEqual(
      {},
    );
  });
});

describe("knotRuntimeConfigEquals", () => {
  it("treats absent and empty as equal per field", () => {
    expect(knotRuntimeConfigEquals({}, { agentId: "", clientUuid: "" })).toBe(
      true,
    );
  });

  it("distinguishes a changed client_uuid", () => {
    expect(
      knotRuntimeConfigEquals(
        { agentId: AGENT_ID },
        { agentId: AGENT_ID, clientUuid: "remote" },
      ),
    ).toBe(false);
  });
});

describe("withKnotAgentId", () => {
  it("preserves an existing client_uuid when only the agent id changes", () => {
    const existing = { knot: { agent_id: "old", client_uuid: "remote" } };
    expect(withKnotAgentId(existing, AGENT_ID)).toEqual({
      knot: { agent_id: AGENT_ID, client_uuid: "remote" },
    });
  });

  it("keeps client_uuid even when the agent id is cleared", () => {
    const existing = { knot: { agent_id: AGENT_ID, client_uuid: "remote" } };
    expect(withKnotAgentId(existing, "")).toEqual({
      knot: { client_uuid: "remote" },
    });
  });

  it("does not disturb another provider's block", () => {
    const existing = { openclaw: { mode: "gateway" } };
    expect(withKnotAgentId(existing, AGENT_ID)).toEqual({
      openclaw: { mode: "gateway" },
      knot: { agent_id: AGENT_ID },
    });
  });
});

describe("withKnotClientUuid", () => {
  it("sets client_uuid while preserving an existing agent id", () => {
    const existing = { knot: { agent_id: AGENT_ID } };
    expect(withKnotClientUuid(existing, "remote")).toEqual({
      knot: { agent_id: AGENT_ID, client_uuid: "remote" },
    });
  });

  it("clears client_uuid on an empty value, keeping the agent id", () => {
    const existing = { knot: { agent_id: AGENT_ID, client_uuid: "remote" } };
    expect(withKnotClientUuid(existing, "")).toEqual({
      knot: { agent_id: AGENT_ID },
    });
  });
});

describe("requested Knot tool target", () => {
  it("distinguishes strict local, platform selection, explicit client and invalid targets", () => {
    expect(knotToolTarget({})).toBe("local");
    expect(knotToolTarget({ knot: { client_uuid: "Remote" } })).toBe("remote");
    expect(knotToolTarget({ knot: { client_uuid: CLIENT_UUID } })).toBe("client");
    expect(knotToolTarget({ knot: { client_uuid: "typo" } })).toBe("invalid");
    expect(knotToolTarget({ knot: { client_uuid: 123 } })).toBe("invalid");
    expect(knotToolTarget([])).toBe("invalid");
  });
});
