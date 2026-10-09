// @vitest-environment node
import { describe, expect, it } from "vitest";
import { normalizeWebUrl, parseWebLinks } from "./models";

describe("website URLs", () => {
  it("accepts local Jenkins and HTTPS URLs without dropping ports, paths or queries", () => {
    expect(normalizeWebUrl(" http://192.168.1.20:8080/job/build?tab=all ")).toBe("http://192.168.1.20:8080/job/build?tab=all");
    expect(normalizeWebUrl("https://EXAMPLE.com")).toBe("https://example.com/");
  });
  it.each(["javascript:alert(1)", "file:///etc/passwd", "data:text/html,x", "https://user:secret@example.com", "", "jenkins", null])("rejects unsafe or invalid input %s", (input) => {
    expect(normalizeWebUrl(input)).toBeNull();
  });
  it("filters malformed and duplicate saved links instead of crashing the page", () => {
    expect(parseWebLinks([null, {}, { id: "1", name: " Jenkins ", url: "http://localhost:8080" },
      { id: "1", name: "Duplicate", url: "https://example.com" }, { id: "2", name: "Bad", url: "javascript:alert(1)" }]))
      .toEqual([{ id: "1", name: "Jenkins", url: "http://localhost:8080/" }]);
  });
});
