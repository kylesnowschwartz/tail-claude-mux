import { describe, test, expect } from "bun:test";
import { createRefocusGate, type RefocusScheduler, type RefocusTimer } from "./refocus-gate";

// --- Fake clock --------------------------------------------------------------
// Deterministic time travel for the gate's timer semantics. The gate accepts a
// scheduler abstraction so tests don't have to wait wall-clock ms.

interface FakeJob {
  id: number;
  fireAt: number;
  cb: () => void;
  cancelled: boolean;
}

function makeFakeClock(): { scheduler: RefocusScheduler; advance: (ms: number) => void; now: () => number } {
  let now = 0;
  let nextId = 1;
  const jobs: FakeJob[] = [];

  const scheduler: RefocusScheduler = {
    setTimeout(cb, ms) {
      const job: FakeJob = { id: nextId++, fireAt: now + ms, cb, cancelled: false };
      jobs.push(job);
      // Return the id boxed as the timer (we only use it as an opaque handle).
      return job.id as unknown as RefocusTimer;
    },
    clearTimeout(t) {
      const id = t as unknown as number;
      const job = jobs.find((j) => j.id === id);
      if (job) job.cancelled = true;
    },
  };

  function advance(ms: number) {
    const target = now + ms;
    while (true) {
      const due = jobs
        .filter((j) => !j.cancelled && j.fireAt <= target)
        .sort((a, b) => a.fireAt - b.fireAt)[0];
      if (!due) break;
      due.cancelled = true;
      now = due.fireAt;
      due.cb();
    }
    now = target;
  }

  return { scheduler, advance, now: () => now };
}

// --- Tests ------------------------------------------------------------------

describe("createRefocusGate", () => {
  test("refocuses after fallbackMs when no capability event ever arrives", () => {
    const { scheduler, advance } = makeFakeClock();
    let calls = 0;
    const gate = createRefocusGate(() => { calls++; }, { quietMs: 250, fallbackMs: 2000, scheduler });

    advance(1999);
    expect(calls).toBe(0); // not yet
    advance(1);
    expect(calls).toBe(1); // fallback fired
    void gate; // unused, but keep the gate alive for the duration
  });

  test("LEAK SCENARIO: capability events at t=0 and t=200 must not refocus until events go quiet", () => {
    // This is the leak we're plugging. Naive impl: refocuses at t=0 on first
    // event. The kitty graphics response arriving at t=200 then lands in the
    // main pane's stdin because tmux focus already moved.
    // Desired: refocus only after quietMs of silence past the LAST event.
    const { scheduler, advance } = makeFakeClock();
    let calls = 0;
    let firedAt = -1;
    const gate = createRefocusGate(() => { calls++; firedAt = scheduler === scheduler ? -1 : -1; }, {
      quietMs: 250, fallbackMs: 2000, scheduler,
    });
    // Use a closure that captures `now` from the clock for assertions:
    let lastFireTime = -1;
    const observe = (fn: () => void): (() => void) => () => { lastFireTime = (scheduler as any)._now?.() ?? lastFireTime; fn(); };
    void observe; void firedAt; // not needed — we infer from `calls` + `advance` order

    gate.onCapability();  // t=0, mode-1016 response
    expect(calls).toBe(0); // MUST NOT have fired yet
    advance(200);
    gate.onCapability();  // t=200, kitty response
    expect(calls).toBe(0); // STILL must not have fired
    advance(249);
    expect(calls).toBe(0); // 449ms total, last event at 200, quiet window incomplete
    advance(1);
    expect(calls).toBe(1); // 450ms total — 250ms past last event — NOW fires
    void lastFireTime;
  });

  test("single capability event: refocus fires quietMs after the event, not on the event itself", () => {
    const { scheduler, advance } = makeFakeClock();
    let calls = 0;
    const gate = createRefocusGate(() => { calls++; }, { quietMs: 250, fallbackMs: 2000, scheduler });

    gate.onCapability();
    expect(calls).toBe(0);
    advance(249);
    expect(calls).toBe(0);
    advance(1);
    expect(calls).toBe(1);
    void gate;
  });

  test("multiple events keep deferring; refocus only after they stop", () => {
    const { scheduler, advance } = makeFakeClock();
    let calls = 0;
    const gate = createRefocusGate(() => { calls++; }, { quietMs: 250, fallbackMs: 2000, scheduler });

    gate.onCapability(); advance(100);
    gate.onCapability(); advance(100);
    gate.onCapability(); advance(100);
    gate.onCapability(); // t=300
    advance(249);        // t=549; 249 since last event
    expect(calls).toBe(0);
    advance(1);          // t=550; 250 since last event
    expect(calls).toBe(1);
    void gate;
  });

  test("cleanup before fire suppresses refocus permanently", () => {
    const { scheduler, advance } = makeFakeClock();
    let calls = 0;
    const gate = createRefocusGate(() => { calls++; }, { quietMs: 250, fallbackMs: 2000, scheduler });

    gate.onCapability();
    gate.cleanup();
    advance(10_000);
    expect(calls).toBe(0);
  });

  test("refocus fires at most once even under a flood of events", () => {
    const { scheduler, advance } = makeFakeClock();
    let calls = 0;
    const gate = createRefocusGate(() => { calls++; }, { quietMs: 250, fallbackMs: 2000, scheduler });

    for (let i = 0; i < 50; i++) {
      gate.onCapability();
      advance(10);
    }
    advance(300);
    expect(calls).toBe(1);
    // Late events after fire should be ignored.
    gate.onCapability();
    advance(1000);
    expect(calls).toBe(1);
  });
});
