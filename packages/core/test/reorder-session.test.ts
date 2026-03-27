import { describe, test, expect } from "bun:test";
import type { ClientCommand } from "../src/shared";

describe("reorder-session command", () => {
  test("ClientCommand union accepts reorder-session type", () => {
    const cmd: ClientCommand = { type: "reorder-session", name: "my-session", delta: -1 };
    expect(cmd.type).toBe("reorder-session");
    expect(cmd).toHaveProperty("name", "my-session");
    expect(cmd).toHaveProperty("delta", -1);
  });

  test("reorder-session delta can be 1 (move down)", () => {
    const cmd: ClientCommand = { type: "reorder-session", name: "other", delta: 1 };
    expect(cmd.delta).toBe(1);
  });

  test("ClientCommand union accepts show-all-sessions type", () => {
    const cmd: ClientCommand = { type: "show-all-sessions" };
    expect(cmd.type).toBe("show-all-sessions");
  });
});
