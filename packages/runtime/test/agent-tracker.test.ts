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

  test("applyEvent marks waiting as unseen when session not active", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "waiting" }));

    expect(tracker.getUnseen()).toContain("sess-1");
  });

  test("applyEvent does NOT mark waiting as unseen when session is active", () => {
    tracker.setActiveSessions(["sess-1"]);
    tracker.applyEvent(event({ session: "sess-1", status: "waiting" }));

    expect(tracker.getUnseen()).not.toContain("sess-1");
  });

  test("applyEvent clears waiting unseen when same instance transitions to running", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "waiting", threadId: "t1" }));
    expect(tracker.getUnseen()).toContain("sess-1");

    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t1" }));
    expect(tracker.getUnseen()).not.toContain("sess-1");
  });

  test("applyEvent: waiting unseen on instance A, running on instance B — A stays unseen", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "waiting", threadId: "t1" }));
    expect(tracker.isUnseen("sess-1")).toBe(true);

    // Instance B is running — should NOT clear instance A's unseen
    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t2" }));
    expect(tracker.isUnseen("sess-1")).toBe(true); // t1 still unseen
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

  // --- ended flag ---

  test("applyEvent with ended=true removes the instance immediately", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t1" }));
    expect(tracker.getAgents("sess-1")).toHaveLength(1);

    tracker.applyEvent(event({ session: "sess-1", status: "done", threadId: "t1", ended: true }));

    expect(tracker.getAgents("sess-1")).toHaveLength(0);
    expect(tracker.isUnseen("sess-1")).toBe(false);
  });

  test("applyEvent with ended=true bypasses the terminal-prune window", () => {
    // Simulates the SessionEnd-after-Stop case: tracker already holds a done
    // entry marked unseen; the ended flag must clear it without waiting.
    tracker.applyEvent(event({ session: "sess-1", status: "done", threadId: "t1" }));
    expect(tracker.isUnseen("sess-1")).toBe(true);

    tracker.applyEvent(event({ session: "sess-1", status: "done", threadId: "t1", ended: true }));

    expect(tracker.getAgents("sess-1")).toHaveLength(0);
    expect(tracker.isUnseen("sess-1")).toBe(false);
  });

  test("applyEvent with ended=true only removes the targeted instance", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t1" }));
    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t2" }));

    tracker.applyEvent(event({ session: "sess-1", status: "done", threadId: "t1", ended: true }));

    const remaining = tracker.getAgents("sess-1");
    expect(remaining).toHaveLength(1);
    expect(remaining[0].threadId).toBe("t2");
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

  test("getAgents returns oldest items first by first-seen", () => {
    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t1", ts: 100 }));
    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t2", ts: 200 }));

    const agents = tracker.getAgents("sess-1");

    expect(agents.map((agent) => agent.threadId)).toEqual(["t1", "t2"]);
  });

  test("getAgents order is stable across status updates on existing instances", () => {
    // Two agents arrive in order t1, t2.
    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t1", ts: 100 }));
    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t2", ts: 200 }));

    // t1 then fires a fresher status update — its ts is now newer than t2's.
    tracker.applyEvent(event({ session: "sess-1", status: "running", threadId: "t1", ts: 300 }));

    // Sort is by first-seen, so t1 must stay above t2; the focused-panel
    // list must not reshuffle when an existing instance pings.
    const agents = tracker.getAgents("sess-1");
    expect(agents.map((agent) => agent.threadId)).toEqual(["t1", "t2"]);
  });

  // --- pruneTerminal ---

  test("pruneTerminal removes seen terminal instances after timeout when pane exited", () => {
    const oldTs = Date.now() - 6 * 60 * 1000; // 6 min ago, past TERMINAL_PRUNE_MS
    tracker.applyEvent(event({ session: "sess-1", status: "done", ts: oldTs, liveness: "exited" }));
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

      // Pane disappears for one scan — held alive within hysteresis grace.
      const firstChanged = tracker.applyPanePresence("sess-1", []);
      expect(firstChanged).toBe(false);
      expect(tracker.getAgents("sess-1")[0]!.liveness).toBe("alive");

      // Second consecutive miss → threshold crossed, transition to exited.
      const secondChanged = tracker.applyPanePresence("sess-1", []);

      expect(secondChanged).toBe(true);
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

      // Pane disappears — first miss is held within hysteresis grace.
      tracker.applyPanePresence("sess-1", []);
      expect(tracker.getAgents("sess-1").length).toBe(1);

      // Second consecutive miss → synthetic deleted.
      const changed = tracker.applyPanePresence("sess-1", []);

      expect(changed).toBe(true);
      expect(tracker.getAgents("sess-1").length).toBe(0);
    });

    test("does not transition unknown-liveness running agents to exited", () => {
      // A "running" entry may be mid-stream before the pane scanner sees it
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));

      const changed = tracker.applyPanePresence("sess-1", []);

      expect(changed).toBe(false);
      const agents = tracker.getAgents("sess-1");
      expect(agents[0]!.liveness).toBeUndefined(); // still unknown — not safe to assume exited
    });

    test("transitions unknown-liveness terminal agents to exited (seed ghosts)", () => {
      // Cold-start seed creates entries with null liveness for sessions found in JSONL.
      // If the pane scanner runs and finds no matching pane, these are dead.
      for (const status of ["done", "interrupted"] as const) {
        const tid = `ghost-${status}`;
        tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: tid, status }));
      }

      const changed = tracker.applyPanePresence("sess-1", []);

      expect(changed).toBe(true);
      const agents = tracker.getAgents("sess-1");
      expect(agents.length).toBe(2);
      for (const a of agents) {
        expect(a.liveness).toBe("exited");
      }
    });

    test("transitions unknown-liveness idle/waiting agents to exited (seed ghosts)", () => {
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "idle-ghost", status: "idle" }));
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "waiting-ghost", status: "waiting" }));

      const changed = tracker.applyPanePresence("sess-1", []);

      expect(changed).toBe(true);
      const agents = tracker.getAgents("sess-1");
      for (const a of agents) {
        expect(a.liveness).toBe("exited");
      }
    });

    test("does not mark claimed entries as exited even with terminal status", () => {
      // Agent has terminal status but pane scanner found a matching pane — still alive
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "alive-done", status: "done" }));

      const changed = tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%10" },
      ]);

      expect(changed).toBe(true);
      const agents = tracker.getAgents("sess-1");
      expect(agents[0]!.liveness).toBe("alive");
      expect(agents[0]!.paneId).toBe("%10");
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

      // Make alive then exited (two consecutive empty scans cross hysteresis)
      tracker.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);
      tracker.applyPanePresence("sess-1", []); // first miss — held alive
      tracker.applyPanePresence("sess-1", []); // second miss — exits

      tracker.pruneStuck(3 * 60 * 1000);

      expect(tracker.getState("sess-1")).toBeNull();
    });

    test("pruneTerminal skips entries with unknown liveness (no pane scan yet)", () => {
      const oldTs = Date.now() - 6 * 60 * 1000;
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "done", ts: oldTs }));
      tracker.markSeen("sess-1");

      // No pane scan has run — liveness is undefined
      tracker.pruneTerminal();

      // Should survive: unknown liveness means we can't confirm the pane is gone
      expect(tracker.getAgents("sess-1").length).toBe(1);
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

    test("two panes of same agent match distinct watcher entries (no spurious idle)", () => {
      // Two Claude Code instances in the same session, each with a watcher entry
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "thread-aaa", status: "done" }));
      tracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "thread-bbb", status: "running" }));

      // Pane scanner finds two claude-code panes
      const changed = tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%10" },
        { agent: "claude-code", paneId: "%11" },
      ]);

      expect(changed).toBe(true);
      const agents = tracker.getAgents("sess-1");

      // Both watcher entries should be enriched — no synthetic "idle" entry created
      expect(agents.length).toBe(2);
      const statuses = agents.map((a) => a.status).sort();
      expect(statuses).toEqual(["done", "running"]);

      // Each got a distinct paneId
      const panes = new Set(agents.map((a) => a.paneId));
      expect(panes.size).toBe(2);
      expect(panes.has("%10")).toBe(true);
      expect(panes.has("%11")).toBe(true);

      // No idle synthetics
      expect(agents.every((a) => a.status !== "idle")).toBe(true);
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

    test("suppresses synthetic creation for ~5s after SessionEnd on the same pane", () => {
      // Simulate the /exit race: SessionEnd hook fires while ps still shows
      // claude in the pane for a beat. Without suppression, the next pane
      // scan mints a ghost synthetic that lingers until the miss counter
      // drops it ~6s later.
      let mockNow = 1_000_000;
      const localTracker = new AgentTracker({ now: () => mockNow });

      // Watcher entry established with a paneId via the normal flow.
      localTracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      localTracker.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%50" }]);
      expect(localTracker.getAgents("sess-1").length).toBe(1);

      // SessionEnd fires — entry removed.
      localTracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "done", ended: true }));
      expect(localTracker.getAgents("sess-1").length).toBe(0);

      // Pane scan within the suppression window still sees claude (exit cleanup).
      // Synthetic must NOT be created.
      mockNow += 1_000;
      localTracker.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%50" }]);
      expect(localTracker.getAgents("sess-1").length).toBe(0);

      mockNow += 3_000; // total +4s, still within 5s window
      localTracker.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%50" }]);
      expect(localTracker.getAgents("sess-1").length).toBe(0);

      // Past the window, scanner is allowed to mint a synthetic again
      // (e.g. a freshly-launched claude in the same pane).
      mockNow += 2_000; // total +6s
      localTracker.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%50" }]);
      const agents = localTracker.getAgents("sess-1");
      expect(agents.length).toBe(1);
      expect(agents[0]!.threadId).toBeUndefined(); // synthetic, not a watcher entry
      expect(agents[0]!.paneId).toBe("%50");
    });

    test("end-suppression is per pane: other panes in same session unaffected", () => {
      let mockNow = 1_000_000;
      const localTracker = new AgentTracker({ now: () => mockNow });

      localTracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      localTracker.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%60" }]);

      // /exit on pane %60
      localTracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "done", ended: true }));

      // Pane scan sees claude in %60 (residue) AND in %61 (a different live CC).
      // %60's synthetic must stay suppressed; %61 must mint its synthetic.
      mockNow += 500;
      localTracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%60" },
        { agent: "claude-code", paneId: "%61" },
      ]);

      const agents = localTracker.getAgents("sess-1");
      expect(agents.length).toBe(1);
      expect(agents[0]!.paneId).toBe("%61");
    });

    test("watcher applyEvent leaves synthetics for OTHER panes intact", () => {
      // Two claude processes in the same tmux session: my watcher (pane %40)
      // and a second claude (pane %41) that hasn't fired hooks yet.
      // Scanner creates two synthetics first.
      tracker.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%40" },
        { agent: "claude-code", paneId: "%41" },
      ]);
      expect(tracker.getAgents("sess-1").length).toBe(2);

      // My watcher arrives — graduates ONE synthetic (the one for my pane),
      // must leave the other intact. Pre-fix bug: it nuked both, then the
      // pane scanner re-created %41's synthetic on its next tick → flicker.
      tracker.applyEvent(event({
        session: "sess-1",
        agent: "claude-code",
        threadId: "mine",
        status: "running",
      }));

      const agents = tracker.getAgents("sess-1");
      expect(agents.length).toBe(2);

      const mine = agents.find((a) => a.threadId === "mine")!;
      const other = agents.find((a) => !a.threadId)!;

      expect(mine.paneId === "%40" || mine.paneId === "%41").toBe(true);
      expect(other).toBeDefined();
      expect(other.paneId).not.toBe(mine.paneId);
      expect(other.liveness).toBe("alive");
    });

    test("does not resurrect sweep-exited entry; creates synthetic for new pane occupant", () => {
      // A tracker that pretends pid 42 is dead and pid 99 is alive.
      const localTracker = new AgentTracker({ isPidAlive: (pid) => pid === 99 });

      // Watcher entry for the original CC at pane %30 with pid 42.
      localTracker.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "old-thread", status: "running", pid: 42 }));
      localTracker.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%30" }]);

      // Sweep notices pid 42 is gone and flips liveness=exited on the watcher entry.
      localTracker.runLivenessSweepOnce();
      const afterSweep = localTracker.getAgents("sess-1").find((a) => a.threadId === "old-thread")!;
      expect(afterSweep.liveness).toBe("exited");

      // Pane %30 still has a claude (a *different* CC). Scanner must NOT
      // resurrect the dead entry — it should create a synthetic instead, so
      // the row stays exited until prune and the fresh CC gets its own slot
      // when its first hook arrives.
      localTracker.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%30" }]);

      const agents = localTracker.getAgents("sess-1");
      const dead = agents.find((a) => a.threadId === "old-thread")!;
      const synthetic = agents.find((a) => !a.threadId)!;

      expect(dead.liveness).toBe("exited");           // still dead, not flipped back
      expect(synthetic).toBeDefined();                 // synthetic created for the pane
      expect(synthetic.paneId).toBe("%30");
      expect(synthetic.liveness).toBe("alive");
    });
  });

  // Pane-presence hysteresis: a single missed scan must not transition an
  // alive entry to exited. Bug source — process tree races during agent
  // re-execs (Claude Code compaction, codex sandbox spawn). See blueprint
  // §V1, ticket TMUX-HEADER-001.
  describe("applyPanePresence — hysteresis (transient miss handling)", () => {
    test("T1: single missed scan holds entry alive (under threshold)", () => {
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);
      expect(t.getAgents("sess-1")[0]!.liveness).toBe("alive");

      const changed = t.applyPanePresence("sess-1", []);

      expect(changed).toBe(false);
      const agent = t.getAgents("sess-1")[0]!;
      expect(agent.liveness).toBe("alive");
      expect(agent.paneId).toBe("%1");
    });

    test("T2: two consecutive misses cross threshold and transition to exited", () => {
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);

      const first = t.applyPanePresence("sess-1", []);
      expect(first).toBe(false);
      expect(t.getAgents("sess-1")[0]!.liveness).toBe("alive");

      const second = t.applyPanePresence("sess-1", []);
      expect(second).toBe(true);
      const agent = t.getAgents("sess-1")[0]!;
      expect(agent.liveness).toBe("exited");
      expect(agent.paneId).toBeUndefined();
    });

    test("T3: re-appearance during grace clears miss counter, no flicker", () => {
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);

      // Simulate a long flap loop: alternate empty/present scans 5 times.
      for (let i = 0; i < 5; i++) {
        t.applyPanePresence("sess-1", []); // single miss — held alive
        t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);
        expect(t.getAgents("sess-1")[0]!.liveness).toBe("alive");
      }
    });

    test("T4: pane move (same agent name, new paneId) does not count as a miss", () => {
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);
      expect(t.getAgents("sess-1")[0]!.paneId).toBe("%1");

      // Pane "moves" — agent name reappears on a different paneId.
      const changed = t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%2" }]);
      expect(changed).toBe(true); // paneId rebound
      const agent = t.getAgents("sess-1")[0]!;
      expect(agent.liveness).toBe("alive");
      expect(agent.paneId).toBe("%2");

      // And no exit transition is queued: a subsequent missing scan is treated
      // as the FIRST miss of a new lifecycle, not the second.
      expect(t.applyPanePresence("sess-1", [])).toBe(false);
      expect(t.getAgents("sess-1")[0]!.liveness).toBe("alive");
    });

    test("T5: ended:true watcher event overrides hysteresis (immediate exit)", () => {
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);

      // Pane disappears — single miss, would normally hold alive.
      t.applyPanePresence("sess-1", []);
      expect(t.getAgents("sess-1")[0]!.liveness).toBe("alive");

      // Definitive exit signal arrives. Entry must be removed immediately.
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "done", ended: true }));
      expect(t.getAgents("sess-1").length).toBe(0);
    });

    test("T6: multi-instance same agent — exiting instance accrues misses while survivor stays alive", () => {
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "thread-A", status: "running" }));
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "thread-B", status: "running" }));
      t.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%1" },
        { agent: "claude-code", paneId: "%2" },
      ]);
      // Threads A and B may have rebound to either pane — capture the binding.
      const initial = t.getAgents("sess-1");
      const findByThread = (tid: string) => initial.find((a) => a.threadId === tid)!;
      const aPane = findByThread("thread-A").paneId!;
      const bPane = findByThread("thread-B").paneId!;
      expect(new Set([aPane, bPane])).toEqual(new Set(["%1", "%2"]));

      // Survivor is whichever pane is still in the next scan; the other instance
      // is the one that exits. Pick the survivor by paneId.
      const survivorPane = "%1";
      const exitingPane = survivorPane === aPane ? bPane : aPane;
      const survivorThread = aPane === survivorPane ? "thread-A" : "thread-B";
      const exitingThread = survivorThread === "thread-A" ? "thread-B" : "thread-A";

      // Scan 1 — only the survivor reports. The exiting instance's paneId
      // (`%2`) is not in scan; the only pa (`%1`) is bound to the survivor's
      // existing paneId, so there are zero spare panes for rebinding. The
      // exiting instance must accrue a real miss (held alive within grace).
      t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: survivorPane }]);
      const afterFirstMiss = t.getAgents("sess-1");
      expect(afterFirstMiss.length).toBe(2);
      expect(afterFirstMiss.find((a) => a.threadId === survivorThread)!.liveness).toBe("alive");
      expect(afterFirstMiss.find((a) => a.threadId === survivorThread)!.paneId).toBe(survivorPane);
      expect(afterFirstMiss.find((a) => a.threadId === exitingThread)!.liveness).toBe("alive");
      // Exiting instance still holds its old paneId (within hysteresis grace).
      expect(afterFirstMiss.find((a) => a.threadId === exitingThread)!.paneId).toBe(exitingPane);

      // Scan 2 — same shape. Threshold (default 2) crosses — exiting instance
      // transitions to exited. Survivor untouched.
      const changed = t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: survivorPane }]);
      expect(changed).toBe(true);
      const afterSecondMiss = t.getAgents("sess-1");
      expect(afterSecondMiss.find((a) => a.threadId === survivorThread)!.liveness).toBe("alive");
      expect(afterSecondMiss.find((a) => a.threadId === survivorThread)!.paneId).toBe(survivorPane);
      expect(afterSecondMiss.find((a) => a.threadId === exitingThread)!.liveness).toBe("exited");
      expect(afterSecondMiss.find((a) => a.threadId === exitingThread)!.paneId).toBeUndefined();
    });

    test("T6b: multi-instance pane move — single survivor, single new paneId, no false miss", () => {
      // Single instance whose pane moves %1 → %2 must be treated as a pane
      // move, not a miss, even when the agent name index would otherwise
      // double-count.
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);
      const before = t.getAgents("sess-1")[0]!;
      expect(before.paneId).toBe("%1");

      // Pane moves: scan returns %2 only.
      const changed = t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%2" }]);
      expect(changed).toBe(true);
      const after = t.getAgents("sess-1");
      expect(after.length).toBe(1);
      expect(after[0]!.liveness).toBe("alive");
      expect(after[0]!.paneId).toBe("%2");
    });

    test("T7: configurable missThreshold preserves test determinism", () => {
      const t = new AgentTracker({ missThreshold: 1 });
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);

      // With threshold=1, single miss exits immediately (matches old behaviour).
      const changed = t.applyPanePresence("sess-1", []);
      expect(changed).toBe(true);
      expect(t.getAgents("sess-1")[0]!.liveness).toBe("exited");
    });

    test("T8: seed ghost path unchanged (immediate exit on first scan)", () => {
      const t = new AgentTracker();
      // Seed entry: applied with seed=true, has no paneId, liveness undefined.
      t.applyEvent(
        event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "idle" }),
        { seed: true },
      );
      // Liveness unset (seed never enriched by scanner)
      expect(t.getAgents("sess-1")[0]!.liveness).toBeUndefined();

      // Scanner runs, finds different panes (no claude-code) → seed ghost
      // transitions to exited via step 3 (NOT step 1 / hysteresis).
      const changed = t.applyPanePresence("sess-1", [{ agent: "pi", paneId: "%9" }]);
      expect(changed).toBe(true);
      const agent = t.getAgents("sess-1")[0]!;
      expect(agent.liveness).toBe("exited");
    });

    test("miss state is cleared on dismiss()", () => {
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);
      t.applyPanePresence("sess-1", []); // accrue one miss

      t.dismiss("sess-1", "claude-code", "abc");
      // Re-create the same instance and verify the counter is fresh.
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      t.applyPanePresence("sess-1", [{ agent: "claude-code", paneId: "%1" }]);

      // First miss after re-create should still hold alive (counter starts at 0).
      const changed = t.applyPanePresence("sess-1", []);
      expect(changed).toBe(false);
      expect(t.getAgents("sess-1")[0]!.liveness).toBe("alive");
    });
  });

  // --- runLivenessSweepOnce ---

  describe("runLivenessSweepOnce", () => {
    test("marks instances with dead pid as liveness='exited'", () => {
      const alive = new Set<number>([200, 300]);
      const t = new AgentTracker({ isPidAlive: (pid) => alive.has(pid) });
      t.applyEvent(event({ session: "s1", agent: "claude-code", threadId: "a", status: "running", pid: 200 }));
      t.applyEvent(event({ session: "s1", agent: "claude-code", threadId: "b", status: "running", pid: 999 }));

      const changed = t.runLivenessSweepOnce();
      expect(changed).toBe(true);

      const agents = t.getAgents("s1");
      const a = agents.find((e) => e.threadId === "a")!;
      const b = agents.find((e) => e.threadId === "b")!;
      expect(a.liveness).toBeUndefined(); // alive — no change
      expect(b.liveness).toBe("exited");  // dead — marked
    });

    test("leaves alive pid alone", () => {
      const t = new AgentTracker({ isPidAlive: () => true });
      t.applyEvent(event({ session: "s1", status: "running", pid: 200 }));
      const changed = t.runLivenessSweepOnce();
      expect(changed).toBe(false);
      expect(t.getState("s1")!.liveness).toBeUndefined();
    });

    test("skips instances without pid", () => {
      const t = new AgentTracker({ isPidAlive: () => false });
      t.applyEvent(event({ session: "s1", status: "running" })); // no pid
      const changed = t.runLivenessSweepOnce();
      expect(changed).toBe(false);
    });

    test("skips instances in a terminal status", () => {
      // A done/error/interrupted instance keeps its current liveness — the
      // sweep is for transitioning alive→exited, not for re-marking already
      // done sessions.
      const t = new AgentTracker({ isPidAlive: () => false });
      t.applyEvent(event({ session: "s1", status: "done", pid: 999, liveness: "alive" }));
      const changed = t.runLivenessSweepOnce();
      expect(changed).toBe(false);
      expect(t.getState("s1")!.liveness).toBe("alive");
    });

    test("skips instances already marked exited (idempotent)", () => {
      const t = new AgentTracker({ isPidAlive: () => false });
      t.applyEvent(event({ session: "s1", status: "running", pid: 999, liveness: "exited" }));
      const changed = t.runLivenessSweepOnce();
      expect(changed).toBe(false);
    });

    test("handles multiple sessions independently", () => {
      const alive = new Set<number>([200]);
      const t = new AgentTracker({ isPidAlive: (pid) => alive.has(pid) });
      t.applyEvent(event({ session: "s1", status: "running", pid: 200 }));
      t.applyEvent(event({ session: "s2", status: "running", pid: 999 }));

      t.runLivenessSweepOnce();
      expect(t.getState("s1")!.liveness).toBeUndefined();
      expect(t.getState("s2")!.liveness).toBe("exited");
    });
  });

  // --- startLivenessCheck / stopLivenessCheck ---

  describe("startLivenessCheck / stopLivenessCheck", () => {
    test("start twice is a no-op (no leaked interval)", () => {
      const t = new AgentTracker({ isPidAlive: () => true });
      t.startLivenessCheck(60_000);
      t.startLivenessCheck(60_000); // idempotent
      t.stopLivenessCheck();
      // No assertion needed — if the timer leaked, Bun's test runner would
      // refuse to exit.
    });

    test("stop is safe when never started", () => {
      const t = new AgentTracker({ isPidAlive: () => true });
      t.stopLivenessCheck(); // no-op
    });
  });

  // --- subagent preservation ---

  describe("subagent preservation", () => {
    test("preserves prior subagent when incoming event has undefined", () => {
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", threadId: "t1", status: "running", subagent: "rb-orchestrator" }));

      // PostToolUse-style event with subagent absent — should not blank the field
      t.applyEvent(event({ session: "sess-1", threadId: "t1", status: "running" }));

      const agents = t.getAgents("sess-1");
      expect(agents[0]!.subagent).toBe("rb-orchestrator");
    });

    test("overwrites subagent when incoming event provides a new value", () => {
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", threadId: "t1", status: "running", subagent: "rb-orchestrator" }));
      t.applyEvent(event({ session: "sess-1", threadId: "t1", status: "running", subagent: "doc-writer" }));

      expect(t.getAgents("sess-1")[0]!.subagent).toBe("doc-writer");
    });
  });

  // --- windowIndex / paneIndex from pane scanner ---

  describe("windowIndex / paneIndex", () => {
    test("synthetic carries windowIndex and paneIndex from scanner input", () => {
      const t = new AgentTracker();
      t.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%7", windowIndex: 3, paneIndex: 1 },
      ]);

      const a = t.getAgents("sess-1")[0]!;
      expect(a.windowIndex).toBe(3);
      expect(a.paneIndex).toBe(1);
    });

    test("watcher entry adopts windowIndex/paneIndex on pane enrichment", () => {
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      t.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%7", windowIndex: 2, paneIndex: 0 },
      ]);

      const a = t.getAgents("sess-1")[0]!;
      expect(a.windowIndex).toBe(2);
      expect(a.paneIndex).toBe(0);
    });

    test("subsequent watcher event without pane info preserves windowIndex/paneIndex", () => {
      const t = new AgentTracker();
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));
      t.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%7", windowIndex: 2, paneIndex: 0 },
      ]);
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "waiting" }));

      const a = t.getAgents("sess-1")[0]!;
      expect(a.windowIndex).toBe(2);
      expect(a.paneIndex).toBe(0);
    });

    test("synthetic graduation copies windowIndex/paneIndex onto adopting watcher entry", () => {
      const t = new AgentTracker();
      // Pane scanner sees a claude-code in window 4 first — creates a synthetic.
      t.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%9", windowIndex: 4, paneIndex: 2 },
      ]);
      // Then the watcher fires — should adopt the synthetic's pane fields.
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));

      const agents = t.getAgents("sess-1");
      expect(agents.length).toBe(1);
      expect(agents[0]!.threadId).toBe("abc");
      expect(agents[0]!.windowIndex).toBe(4);
      expect(agents[0]!.paneIndex).toBe(2);
    });

    test("getAgents sorts by (windowIndex, paneIndex, firstSeenTs)", () => {
      const t = new AgentTracker();
      // Three watcher entries arrive in firstSeenTs order: t1, t2, t3.
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "t1", status: "running", ts: 100 }));
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "t2", status: "running", ts: 200 }));
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "t3", status: "running", ts: 300 }));

      // Scanner reports them in window/pane order: t2→w1p0, t3→w2p0, t1→w2p1.
      // Pane scanner doesn't know threadIds, so emit three panes; the claim
      // loop picks one unclaimed entry per pane in instance-iteration order
      // (t1, t2, t3 — Map insertion order). We control the assignment by
      // calling applyPanePresence with one pane at a time across separate
      // scans so each pane binds to the next unclaimed entry.
      t.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%1", windowIndex: 2, paneIndex: 1 },
        { agent: "claude-code", paneId: "%2", windowIndex: 1, paneIndex: 0 },
        { agent: "claude-code", paneId: "%3", windowIndex: 2, paneIndex: 0 },
      ]);

      const order = t.getAgents("sess-1").map((a) => a.threadId);
      // Expected: rows sort by (window asc, pane asc) — t2 (w1p0), t3 (w2p0), t1 (w2p1)
      expect(order).toEqual(["t2", "t3", "t1"]);
    });

    test("claim loop prefers PID match over Map iteration order (no crisscross)", () => {
      const t = new AgentTracker();
      // Two claude-code watcher entries — different threadIds, different pids.
      // Insertion order: assistant first, rb-orch second.
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "assistant", status: "running", pid: 1001 }));
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "rb-orch",  status: "running", pid: 2002 }));

      // Scanner emits panes in the OPPOSITE order — rb-orch's pane first.
      // Without pid-aware claiming, the loop would crisscross:
      //   pane %200 (pid 2002) → claims first unclaimed entry = "assistant"
      //   pane %100 (pid 1001) → claims next unclaimed entry = "rb-orch"
      // With pid-aware claiming, each pane claims the entry whose pid matches.
      t.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%200", pid: 2002, windowIndex: 2, paneIndex: 2 },
        { agent: "claude-code", paneId: "%100", pid: 1001, windowIndex: 4, paneIndex: 1 },
      ]);

      const agents = t.getAgents("sess-1");
      const assistant = agents.find((a) => a.threadId === "assistant")!;
      const rbOrch    = agents.find((a) => a.threadId === "rb-orch")!;
      expect(assistant.paneId).toBe("%100");
      expect(assistant.windowIndex).toBe(4);
      expect(rbOrch.paneId).toBe("%200");
      expect(rbOrch.windowIndex).toBe(2);
    });

    test("claim loop falls back to PID-less watcher when no PID match available", () => {
      const t = new AgentTracker();
      // Cold-boot watcher entry — no pid yet (hook hasn't fired).
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running" }));

      // Scanner emits a pane with a pid that doesn't match anything yet —
      // the PID-less entry is a fair fallback target.
      t.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%5", pid: 1234, windowIndex: 1, paneIndex: 0 },
      ]);

      const agents = t.getAgents("sess-1");
      expect(agents.length).toBe(1);
      expect(agents[0]!.threadId).toBe("abc");
      expect(agents[0]!.paneId).toBe("%5");
    });

    test("claim loop refuses to bind to an entry whose pid disagrees", () => {
      const t = new AgentTracker();
      // Watcher resolved pid 5555.
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "abc", status: "running", pid: 5555 }));

      // Scanner sees a different pid (e.g. a freshly-launched second claude
      // whose hook hasn't fired yet) — must NOT claim the pid 5555 entry.
      t.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%9", pid: 9999, windowIndex: 1, paneIndex: 0 },
      ]);

      const agents = t.getAgents("sess-1");
      const watcher = agents.find((a) => a.threadId === "abc")!;
      // Watcher entry must not have been bound to %9.
      expect(watcher.paneId).toBeUndefined();
      // A synthetic should have been minted for the new pane.
      const synthetic = agents.find((a) => a.paneId === "%9")!;
      expect(synthetic).toBeDefined();
      expect(synthetic.pid).toBe(9999);
    });

    test("getAgents puts rows without windowIndex last", () => {
      const t = new AgentTracker();
      // t1 has no pane info; t2 is on window 5.
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "t1", status: "running", ts: 100 }));
      t.applyEvent(event({ session: "sess-1", agent: "claude-code", threadId: "t2", status: "running", ts: 200 }));
      t.applyPanePresence("sess-1", [
        { agent: "claude-code", paneId: "%2", windowIndex: 5, paneIndex: 0 },
      ]);

      // t1 had no pane match — only t2 got enriched. Pane scanner picks the
      // first unclaimed entry in iteration order (t1), so t1 gets the pane.
      // Force the opposite: enrich t2 first by applying pane presence with
      // both present — t1 alphabetically/insertion-first gets the pane.
      // Simpler: just check that the watcher entry with windowIndex sorts
      // before the one without.
      const agents = t.getAgents("sess-1");
      const withWin = agents.find((a) => a.windowIndex !== undefined);
      const without = agents.find((a) => a.windowIndex === undefined);
      expect(withWin).toBeDefined();
      expect(without).toBeDefined();
      const idxWith = agents.indexOf(withWin!);
      const idxWithout = agents.indexOf(without!);
      expect(idxWith).toBeLessThan(idxWithout);
    });
  });
});
