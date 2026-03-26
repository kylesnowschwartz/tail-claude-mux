import { describe, test, expect } from "bun:test";
import { determineStatus } from "../src/agents/watchers/opencode";

describe("OpenCode determineStatus", () => {
  test("returns idle for null message", () => {
    expect(determineStatus(null, [])).toBe("idle");
  });

  test("returns running for user message", () => {
    expect(determineStatus({ role: "user" }, [])).toBe("running");
  });

  test("returns running for assistant with tool-calls finish", () => {
    expect(determineStatus({ role: "assistant", finish: "tool-calls" }, [])).toBe("running");
  });

  test("returns running for assistant with tool parts", () => {
    expect(determineStatus({ role: "assistant" }, [{ type: "tool" }])).toBe("running");
  });

  test("returns waiting for assistant with no tools", () => {
    expect(determineStatus({ role: "assistant" }, [])).toBe("waiting");
  });

  test("returns idle for unknown role", () => {
    expect(determineStatus({ role: "system" }, [])).toBe("idle");
  });
});
