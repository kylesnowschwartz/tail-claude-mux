import { describe, test, expect, beforeEach } from "bun:test";
import { MuxRegistry } from "../src/mux/registry";
import type { MuxProvider } from "../src/contracts/mux";

function fakeMux(name: string): MuxProvider {
  return {
    specificationVersion: "v1",
    name,
    listSessions: () => [],
    switchSession: () => {},
    getCurrentSession: () => null,
    getSessionDir: () => "",
    getPaneCount: () => 1,
    getClientTty: () => "",
    createSession: () => {},
    killSession: () => {},
    setupHooks: () => {},
    cleanupHooks: () => {},
  };
}

describe("MuxRegistry", () => {
  let registry: MuxRegistry;

  beforeEach(() => {
    registry = new MuxRegistry();
  });

  // --- register ---

  test("register adds a provider by name", () => {
    registry.register(fakeMux("tmux"));
    expect(registry.list()).toEqual(["tmux"]);
  });

  test("register multiple providers", () => {
    registry.register(fakeMux("tmux"));
    registry.register(fakeMux("screen"));
    expect(registry.list()).toEqual(["tmux", "screen"]);
  });

  test("register overwrites provider with same name", () => {
    const mux1 = fakeMux("tmux");
    const mux2 = fakeMux("tmux");
    registry.register(mux1);
    registry.register(mux2);
    expect(registry.list()).toEqual(["tmux"]);
    expect(registry.get("tmux")).toBe(mux2);
  });

  // --- get ---

  test("get returns registered provider", () => {
    const mux = fakeMux("tmux");
    registry.register(mux);
    expect(registry.get("tmux")).toBe(mux);
  });

  test("get returns null for unknown provider", () => {
    expect(registry.get("screen")).toBeNull();
  });

  // --- resolve (auto-detect + config override) ---

  test("resolve returns explicitly set provider first", () => {
    const mux = fakeMux("custom");
    registry.register(fakeMux("tmux"));
    registry.register(mux);
    expect(registry.resolve("custom")).toBe(mux);
  });

  test("resolve returns null when requested provider not registered", () => {
    registry.register(fakeMux("tmux"));
    expect(registry.resolve("screen")).toBeNull();
  });

  test("resolve with no preference auto-detects from env", () => {
    registry.register(fakeMux("tmux"));
    // Since we're in a tmux session ($TMUX is set), should resolve tmux
    if (process.env.TMUX) {
      expect(registry.resolve()?.name).toBe("tmux");
    } else {
      // Not in tmux — should return null or first registered
      expect(registry.resolve()).toBeNull();
    }
  });

  // --- list ---

  test("list returns empty array when no providers", () => {
    expect(registry.list()).toEqual([]);
  });
});
