import { describe, test, expect, beforeEach } from "bun:test";
import { AgentTracker } from "../src/agents/tracker";
import type { AgentEvent } from "../src/contracts/agent";

function event(overrides: Partial<AgentEvent> = {}): AgentEvent {
  return {
    agent: "amp",
    session: "sess-1",
    status: "running",
    ts: Date.now(),
    ...overrides,
  };
}

describe("AgentTracker", () => {
  let tracker: AgentTracker;

  beforeEach(() => {
    tracker = new AgentTracker();
  });

  // --- applyEvent ---

  test("applyEvent stores agent state by session", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "running" }));

    const state = tracker.getState("sess-1");
    expect(state).not.toBeNull();
    expect(state!.status).toBe("running");
    expect(state!.agent).toBe("amp");
  });

  test("applyEvent overwrites previous state for same session", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "running" }));
    tracker.applyEvent(event({ session: "sess-1", status: "done" }));

    expect(tracker.getState("sess-1")!.status).toBe("done");
  });

  test("applyEvent marks terminal status as unseen when session not active", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "done" }));

    expect(tracker.getUnseen()).toContain("sess-1");
  });

  test("applyEvent does NOT mark terminal status as unseen when session is active", () => {
    tracker.setActiveSessions(["sess-1"]);
    tracker.applyEvent(event({ session: "sess-1", status: "done" }));

    expect(tracker.getUnseen()).not.toContain("sess-1");
  });

  test("applyEvent clears unseen when same instance transitions to non-terminal", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "done", threadId: "t1" }));
    expect(tracker.getUnseen()).toContain("sess-1");

    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t1" }));
    expect(tracker.getUnseen()).not.toContain("sess-1");
  });

  test("applyEvent: resuming thread A does NOT clear thread B unseen", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "done", threadId: "t1" }));
    tracker.applyEvent(event({ session: "sess-1", status: "done", threadId: "t2" }));
    expect(tracker.isUnseen("sess-1")).toBe(true);

    // Thread A resumes (user interacted) — but thread B is still unseen
    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t1" }));
    expect(tracker.isUnseen("sess-1")).toBe(true); // thread B still unseen
  });

  // --- getState ---

  test("getState returns null for unknown session", () => {
    expect(tracker.getState("unknown")).toBeNull();
  });

  // --- markSeen ---

  test("markSeen clears unseen flag but keeps terminal instances", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "done" }));
    expect(tracker.getUnseen()).toContain("sess-1");

    const cleared = tracker.markSeen("sess-1");
    expect(cleared).toBe(true);
    expect(tracker.getUnseen()).not.toContain("sess-1");
    // Instance still exists (seen terminal), pruneTerminal will clean it up
    expect(tracker.getState("sess-1")).not.toBeNull();
    expect(tracker.getState("sess-1")!.status).toBe("done");
  });

  test("markSeen returns false when session has no unseen", () => {
    expect(tracker.markSeen("nonexistent")).toBe(false);
  });

  test("markSeen does NOT remove state when status is not terminal", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "running" }));
    // Manually add to unseen to test edge case
    const cleared = tracker.markSeen("sess-1");
    expect(cleared).toBe(false);
    expect(tracker.getState("sess-1")).not.toBeNull();
  });

  test("dismiss removes only the targeted agent instance", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "done", agent: "amp", threadId: "t1" }));
    tracker.applyEvent(event({ session: "sess-1", status: "running", agent: "codex", threadId: "t2" }));

    const dismissed = tracker.dismiss("sess-1", "amp", "t1");

    expect(dismissed).toBe(true);
    expect(tracker.getAgents("sess-1").map((agent) => `${agent.agent}:${agent.threadId}`)).toEqual(["codex:t2"]);
    expect(tracker.getUnseen()).not.toContain("sess-1");
  });

  // --- pruneStuck ---

  test("pruneStuck removes running states older than timeout", () => {
    const oldTs = Date.now() - 4 * 60 * 1000; // 4 minutes ago
    tracker.applyEvent(event({ session: "sess-1", status: "running", ts: oldTs }));

    tracker.pruneStuck(3 * 60 * 1000);

    expect(tracker.getState("sess-1")).toBeNull();
    expect(tracker.getUnseen()).not.toContain("sess-1");
  });

  test("pruneStuck does NOT remove recent running states", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "running", ts: Date.now() }));

    tracker.pruneStuck(3 * 60 * 1000);

    expect(tracker.getState("sess-1")).not.toBeNull();
  });

  test("pruneStuck does NOT remove non-running states regardless of age", () => {
    const oldTs = Date.now() - 10 * 60 * 1000;
    tracker.applyEvent(event({ session: "sess-1", status: "done", ts: oldTs }));

    tracker.pruneStuck(3 * 60 * 1000);

    expect(tracker.getState("sess-1")).not.toBeNull();
  });

  // --- isUnseen ---

  test("isUnseen returns correct value", () => {
    expect(tracker.isUnseen("sess-1")).toBe(false);

    tracker.applyEvent(event({ session: "sess-1", status: "error" }));
    expect(tracker.isUnseen("sess-1")).toBe(true);

    tracker.markSeen("sess-1");
    expect(tracker.isUnseen("sess-1")).toBe(false);
  });

  // --- handleFocus ---

  test("handleFocus clears unseen for focused session", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "done" }));
    expect(tracker.isUnseen("sess-1")).toBe(true);

    const hadUnseen = tracker.handleFocus("sess-1");
    expect(hadUnseen).toBe(true);
    expect(tracker.isUnseen("sess-1")).toBe(false);
  });

  test("handleFocus updates active sessions", () => {
    tracker.handleFocus("sess-2");

    // Now sess-2 is active; a terminal event shouldn't mark it unseen
    tracker.applyEvent(event({ session: "sess-2", status: "done" }));
    expect(tracker.isUnseen("sess-2")).toBe(false);
  });

  // --- getAgents unseen flag ---

  test("getAgents stamps unseen flag on terminal instances", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "done", threadId: "t1" }));
    const agents = tracker.getAgents("sess-1");
    expect(agents.length).toBe(1);
    expect(agents[0]!.unseen).toBe(true);
  });

  test("getAgents does not stamp unseen on seen terminal instances", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "done", threadId: "t1" }));
    tracker.markSeen("sess-1");
    const agents = tracker.getAgents("sess-1");
    expect(agents.length).toBe(1);
    expect(agents[0]!.unseen).toBeUndefined();
  });

  // --- getAgents ordering ---

  test("getAgents returns newest items first", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "done", threadId: "t1", ts: 100 }));
    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t2", ts: 200 }));

    const agents = tracker.getAgents("sess-1");

    expect(agents.map((agent) => agent.threadId)).toEqual(["t2", "t1"]);
  });

  // --- pruneTerminal ---

  test("pruneTerminal removes seen terminal instances after timeout", () => {
    const oldTs = Date.now() - 6 * 60 * 1000; // 6 min ago, past TERMINAL_PRUNE_MS
    tracker.applyEvent(event({ session: "sess-1", status: "done", ts: oldTs }));
    tracker.markSeen("sess-1"); // Mark seen so pruneTerminal can remove it

    tracker.pruneTerminal();

    expect(tracker.getState("sess-1")).toBeNull();
  });

  test("pruneTerminal does NOT remove unseen terminal instances", () => {
    const oldTs = Date.now() - 6 * 60 * 1000;
    tracker.applyEvent(event({ session: "sess-1", status: "done", ts: oldTs }));
    // NOT marked seen

    tracker.pruneTerminal();

    expect(tracker.getState("sess-1")).not.toBeNull();
  });

  // --- applyPanePresence ---

  describe("applyPanePresence", () => {
    test("enriches existing watcher entry with paneId and liveness", () => {
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));

      const changed = tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%1" },
      ]);

      expect(changed).toBe(true);
      const agents = tracker.getAgents("sess-1");
      expect(agents.length).toBe(1);
      expect(agents[0]!.paneId).toBe("%1");
      expect(agents[0]!.liveness).toBe("alive");
      // Watcher status preserved — scanner doesn't touch it
      expect(agents[0]!.status).toBe("running");
    });

    test("creates minimal synthetic entry for unmatched pane agent", () => {
      const changed = tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%5" },
      ]);

      expect(changed).toBe(true);
      const agents = tracker.getAgents("sess-1");
      expect(agents.length).toBe(1);
      expect(agents[0]!.agent).toBe("claude-code");
      expect(agents[0]!.paneId).toBe("%5");
      expect(agents[0]!.liveness).toBe("alive");
      expect(agents[0]!.status).toBe("idle"); // default for synthetics
      expect(agents[0]!.threadId).toBeUndefined(); // scanner doesn't resolve threadId
    });

    test("transitions watcher-sourced entry to exited when pane disappears", () => {
      // Watcher creates a real entry, then scanner enriches it
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%1" },
      ]);
      expect(tracker.getAgents("sess-1")[0]!.liveness).toBe("alive");

      // Pane disappears — watcher entry should survive but transition to exited
      const changed = tracker.applyPanePresence("sess-1", []);

      expect(changed).toBe(true);
      const agents = tracker.getAgents("sess-1");
      expect(agents.length).toBe(1);
      expect(agents[0]!.liveness).toBe("exited");
      expect(agents[0]!.paneId).toBeUndefined();
      // Watcher-sourced data preserved
      expect(agents[0]!.threadId).toBe("abc");
      expect(agents[0]!.status).toBe("running");
    });

    test("deletes synthetic entries when their pane disappears", () => {
      // Synthetic created by pane scanner (no watcher data)
      tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%5" },
      ]);
      expect(tracker.getAgents("sess-1").length).toBe(1);
      expect(tracker.getAgents("sess-1")[0]!.status).toBe("idle");

      // Pane disappears — synthetic should be removed entirely, not left as zombie
      const changed = tracker.applyPanePresence("sess-1", []);

      expect(changed).toBe(true);
      expect(tracker.getAgents("sess-1").length).toBe(0);
    });

    test("does not transition unknown-liveness agents to exited", () => {
      // Watcher-only entry (no pane info)
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));

      // Empty pane scan — should NOT affect watcher-only entries
      const changed = tracker.applyPanePresence("sess-1", []);

      expect(changed).toBe(false);
      const agents = tracker.getAgents("sess-1");
      expect(agents[0]!.liveness).toBeUndefined(); // still unknown
    });

    test("pruneStuck skips alive agents", () => {
      const oldTs = Date.now() - 10 * 60 * 1000;
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running", ts: oldTs }));

      // Make it alive via pane presence
      tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%1" },
      ]);

      // Prune with a timeout that would normally remove it
      tracker.pruneStuck(3 * 60 * 1000);

      // Should survive because it's alive
      expect(tracker.getAgents("sess-1").length).toBe(1);
    });

    test("pruneStuck removes exited agents", () => {
      const oldTs = Date.now() - 10 * 60 * 1000;
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running", ts: oldTs }));

      // Make alive then exited
      tracker.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);
      tracker.applyPanePresence("sess-1", []); // exits

      tracker.pruneStuck(3 * 60 * 1000);

      expect(tracker.getState("sess-1")).toBeNull();
    });

    test("pruneTerminal skips alive agents even with terminal status", () => {
      const oldTs = Date.now() - 6 * 60 * 1000;
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "done", ts: oldTs }));
      tracker.markSeen("sess-1"); // Mark seen so prune would normally remove

      // Make it alive
      tracker.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);

      tracker.pruneTerminal();

      // Should survive because alive
      expect(tracker.getAgents("sess-1").length).toBe(1);
    });

    test("returns false when nothing changed", () => {
      tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%1" },
      ]);

      // Apply same presence again
      const changed = tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%1" },
      ]);

      expect(changed).toBe(false);
    });

    test("enriches watcher entry matched by agent name", () => {
      tracker.applyEvent(event({ session: "sess-1", agent: "amp", status: "running" }));

      const changed = tracker.applyPanePresence("sess-1", [
        { agent: "amp", paneId: "%3" },
      ]);

      // Should not create a duplicate — enriches the existing entry
      expect(changed).toBe(true);
      expect(tracker.getAgents("sess-1").length).toBe(1);
      expect(tracker.getAgents("sess-1")[0]!.paneId).toBe("%3");
      expect(tracker.getAgents("sess-1")[0]!.liveness).toBe("alive");
    });

    test("prefers watcher entry over synthetic when enriching", () => {
      // Watcher tracks the current conversation
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "current-convo", status: "running" }));

      // Scanner just reports agent + paneId (no threadId)
      const changed = tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%21" },
      ]);

      expect(changed).toBe(true);
      const agents = tracker.getAgents("sess-1");
      expect(agents.length).toBe(1);
      expect(agents[0]!.threadId).toBe("current-convo"); // watcher's threadId preserved
      expect(agents[0]!.paneId).toBe("%21");
      expect(agents[0]!.liveness).toBe("alive");
    });

    test("cleans up synthetic entry when watcher creates entry for same agent", () => {
      // Scanner detects agent before watcher → creates synthetic
      tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%21" },
      ]);
      expect(tracker.getAgents("sess-1").length).toBe(1);
      expect(tracker.getAgents("sess-1")[0]!.paneId).toBe("%21");

      // Watcher catches up → creates entry with threadId, auto-cleans synthetic
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      const agents = tracker.getAgents("sess-1");
      expect(agents.length).toBe(1);
      expect(agents[0]!.threadId).toBe("abc");
      expect(agents[0]!.paneId).toBe("%21");
      expect(agents[0]!.liveness).toBe("alive");
    });
  });
});
