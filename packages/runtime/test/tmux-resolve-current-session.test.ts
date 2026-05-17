import { describe, expect, test } from "bun:test";
import { resolveCurrentSession, type ClientInfo } from "@tcm/mux-tmux";

// Minimal ClientInfo factory — only the fields resolveCurrentSession reads.
function client(tty: string, sessionName: string): ClientInfo {
  return {
    tty,
    sessionName,
    sessionId: sessionName,
    activity: 0,
    windowId: "",
    paneId: "",
  } as unknown as ClientInfo;
}

describe("resolveCurrentSession", () => {
  test("returns null when no clients attached", () => {
    expect(resolveCurrentSession([])).toBeNull();
  });

  test("returns the lone client's session when exactly one is attached", () => {
    const clients = [client("/dev/ttys001", "main")];
    expect(resolveCurrentSession(clients)).toBe("main");
  });

  test("returns null with multiple clients and no clientTty (refuses to guess)", () => {
    // Previously TmuxClient.getCurrentSession returned `clients[0].sessionName`
    // — whichever client tmux happened to list first. With two clients on two
    // sessions, the caller silently targeted the wrong one. Now: fail closed.
    const clients = [
      client("/dev/ttys001", "alpha"),
      client("/dev/ttys002", "beta"),
    ];
    expect(resolveCurrentSession(clients)).toBeNull();
  });

  test("returns the matching client's session when clientTty is provided", () => {
    const clients = [
      client("/dev/ttys001", "alpha"),
      client("/dev/ttys002", "beta"),
    ];
    expect(resolveCurrentSession(clients, "/dev/ttys002")).toBe("beta");
    expect(resolveCurrentSession(clients, "/dev/ttys001")).toBe("alpha");
  });

  test("returns null when clientTty doesn't match any attached client", () => {
    // Stale TTY (client disconnected between resolution and lookup). Don't
    // fall back to clients[0] — fail closed.
    const clients = [client("/dev/ttys001", "alpha")];
    expect(resolveCurrentSession(clients, "/dev/ttys999")).toBeNull();
  });

  test("clientTty match wins over single-client fallback", () => {
    // Even with one client, an explicit clientTty must match — protects
    // against stale TTYs from prior connections.
    const clients = [client("/dev/ttys001", "alpha")];
    expect(resolveCurrentSession(clients, "/dev/ttys999")).toBeNull();
    expect(resolveCurrentSession(clients, "/dev/ttys001")).toBe("alpha");
  });
});
