// Refocus gate — controls when the sidebar TUI returns input focus to the
// main pane after terminal capability detection.
//
// The leak this gate plugs: opentui emits a "capabilities" event for EACH
// capability response sequence (DECRPM 1016, kitty graphics, DA1, mouse
// modes, ...), not once when all probes settle. The terminal answers each
// probe separately and may stretch the responses out over tens of ms. If we
// refocus the main pane on the first event, late responses (e.g. kitty's
// `\eP=Gi=31337;OK\e\\`) arrive at tmux AFTER focus has moved — and get
// dispatched to the main pane's stdin, where they show up as garbage typed
// into the user's shell.
//
// The gate fires `refocus` exactly once, after either:
//   1. `quietMs` of silence following the most recent capability event, or
//   2. `fallbackMs` from gate creation when no event ever arrives.

export type RefocusTimer = ReturnType<typeof setTimeout>;

export interface RefocusScheduler {
  setTimeout: (cb: () => void, ms: number) => RefocusTimer;
  clearTimeout: (t: RefocusTimer) => void;
}

export interface RefocusGateOpts {
  /** Wait this long after the most recent capability event before refocusing. */
  quietMs: number;
  /** Hard cap when no capability event ever arrives (degraded terminal). */
  fallbackMs: number;
  /** Optional injected scheduler — tests pass a fake clock. */
  scheduler?: RefocusScheduler;
}

export interface RefocusGate {
  /** Called by the opentui "capabilities" listener for each response sequence. */
  onCapability(): void;
  /** Tear down outstanding timers and prevent refocus from firing afterwards. */
  cleanup(): void;
}

export function createRefocusGate(refocus: () => void, opts: RefocusGateOpts): RefocusGate {
  const sched: RefocusScheduler = opts.scheduler ?? {
    setTimeout: globalThis.setTimeout,
    clearTimeout: globalThis.clearTimeout,
  };

  let fired = false;
  let pending: RefocusTimer | null = null;

  function fire() {
    if (fired) return;
    fired = true;
    pending = null;
    refocus();
  }

  function schedule(ms: number) {
    if (fired) return;
    if (pending !== null) sched.clearTimeout(pending);
    pending = sched.setTimeout(fire, ms);
  }

  // Hard fallback at creation time. Reset whenever a capability event arrives
  // (the quietMs delay always preempts the longer fallback).
  schedule(opts.fallbackMs);

  return {
    onCapability() {
      // Push the refocus out by quietMs. Each new event resets the timer;
      // once events stop arriving, the timer fires.
      schedule(opts.quietMs);
    },
    cleanup() {
      fired = true;
      if (pending !== null) {
        sched.clearTimeout(pending);
        pending = null;
      }
    },
  };
}
