import { describe, test, expect } from "bun:test";
import { buildExplain } from "../src/agents/explain";
import type { AgentEvent } from "../src/contracts/agent";
import { RECONCILE_STALE_MS, STUCK_RUNNING_TIMEOUT_MS } from "../src/shared";
import { ALIVE_PRUNE_CEILING_MS, TERMINAL_PRUNE_MS } from "../src/agents/tracker";

const NOW = 1_000_000_000;

function ev(partial: Partial<AgentEvent>): AgentEvent {
  return {
    agent: "claude-code",
    session: "proj",
    status: "running",
    ts: NOW,
    ...partial,
  };
}

function tier(report: ReturnType<typeof buildExplain>, id: string) {
  return report.lifecycle.tiers.find((t) => t.id === id)!;
}

describe("buildExplain — running + alive", () => {
  test("reports reconcile + alive-ceiling tiers, governed by reconcile", () => {
    const report = buildExplain(
      ev({ liveness: "alive", pid: 4242, ts: NOW - 10_000 }),
      NOW,
      null,
    );
    expect(report.status).toBe("running");
    expect(report.liveness).toBe("alive");
    expect(report.pid).toBe(4242);
    expect(report.ageMs).toBe(10_000);

    expect(tier(report, "reconcile").applies).toBe(true);
    expect(tier(report, "reconcile").eligibleInMs).toBe(RECONCILE_STALE_MS - 10_000);
    expect(tier(report, "alive-ceiling").applies).toBe(true);
    expect(tier(report, "alive-ceiling").eligibleInMs).toBe(ALIVE_PRUNE_CEILING_MS - 10_000);
    // prune-stuck (exited-only) and terminal tiers do not apply
    expect(tier(report, "prune-stuck").applies).toBe(false);
    expect(tier(report, "prune-terminal").applies).toBe(false);

    // reconcile fires soonest → governs
    expect(report.lifecycle.governing).toBe("reconcile");
  });

  test("once past the reconcile window the probe is eligible and verdict carries through", () => {
    const report = buildExplain(
      ev({ liveness: "alive", pid: 7, ts: NOW - (RECONCILE_STALE_MS + 5_000) }),
      NOW,
      "working",
    );
    expect(report.probe.eligible).toBe(true);
    expect(report.probe.verdict).toBe("working");
    expect(tier(report, "reconcile").eligibleInMs).toBe(0);
    // alive-ceiling is still the long backstop and now governs (reconcile == 0 too,
    // but reconcile is declared first so it wins the tie)
    expect(report.lifecycle.governing).toBe("reconcile");
  });

  test("running + alive without a pid is not reconciled", () => {
    const report = buildExplain(ev({ liveness: "alive", ts: NOW - 10_000 }), NOW, null);
    expect(tier(report, "reconcile").applies).toBe(false);
    expect(report.probe.eligible).toBe(false);
    // alive-ceiling still governs (the only applicable tier)
    expect(report.lifecycle.governing).toBe("alive-ceiling");
  });
});

describe("buildExplain — running + exited", () => {
  test("governed by prune-stuck with countdown to the stuck timeout", () => {
    const report = buildExplain(
      ev({ liveness: "exited", pid: 9, ts: NOW - 20_000 }),
      NOW,
      null,
    );
    expect(tier(report, "prune-stuck").applies).toBe(true);
    expect(tier(report, "prune-stuck").eligibleInMs).toBe(STUCK_RUNNING_TIMEOUT_MS - 20_000);
    expect(tier(report, "reconcile").applies).toBe(false);
    expect(tier(report, "alive-ceiling").applies).toBe(false);
    expect(report.lifecycle.governing).toBe("prune-stuck");
  });
});

describe("buildExplain — terminal status", () => {
  test("done + exited → terminal-prune countdown", () => {
    const report = buildExplain(
      ev({ status: "done", liveness: "exited", ts: NOW - 60_000 }),
      NOW,
      null,
    );
    expect(tier(report, "prune-terminal").applies).toBe(true);
    expect(tier(report, "prune-terminal").eligibleInMs).toBe(TERMINAL_PRUNE_MS - 60_000);
    expect(report.lifecycle.governing).toBe("prune-terminal");
  });

  test("done + alive → no prune until process exits (stable)", () => {
    const report = buildExplain(
      ev({ status: "done", liveness: "alive", ts: NOW - 60_000 }),
      NOW,
      null,
    );
    expect(tier(report, "prune-terminal").applies).toBe(false);
    expect(report.lifecycle.governing).toBe("stable");
  });
});

describe("buildExplain — idle/waiting", () => {
  test("idle + exited → pruned immediately (eligibleInMs 0)", () => {
    const report = buildExplain(
      ev({ status: "idle", liveness: "exited", ts: NOW - 1_000 }),
      NOW,
      null,
    );
    expect(tier(report, "prune-idle").applies).toBe(true);
    expect(tier(report, "prune-idle").eligibleInMs).toBe(0);
    expect(report.lifecycle.governing).toBe("prune-idle");
  });

  test("idle + alive → stable (pane scanner governs, no prune tier)", () => {
    const report = buildExplain(
      ev({ status: "idle", liveness: "alive", ts: NOW - 1_000 }),
      NOW,
      null,
    );
    expect(report.lifecycle.governing).toBe("stable");
  });
});

describe("buildExplain — field projection", () => {
  test("nulls out absent optional fields and reports present ones", () => {
    const report = buildExplain(
      ev({
        threadId: "thread-1",
        paneId: "%5",
        windowIndex: 2,
        paneIndex: 1,
        toolDescription: "Reading config.ts",
        liveness: "alive",
        pid: 11,
        ts: NOW,
      }),
      NOW,
      null,
    );
    expect(report.threadId).toBe("thread-1");
    expect(report.paneId).toBe("%5");
    expect(report.windowIndex).toBe(2);
    expect(report.paneIndex).toBe(1);
    expect(report.toolDescription).toBe("Reading config.ts");
    expect(report.ageMs).toBe(0);
  });

  test("unknown liveness when none recorded", () => {
    const report = buildExplain(ev({ ts: NOW }), NOW, null);
    expect(report.liveness).toBe("unknown");
    expect(report.pid).toBeNull();
    expect(report.threadId).toBeNull();
  });
});
