// tcm status reporter for the Pi coding agent. Interactive sessions post
// their lifecycle to tcm's /hook endpoint so the tcm sidebar shows live
// status, including waiting on a dialog. Each post times out after 500 ms
// and swallows every error: the extension never throws or prints. Only the
// session_shutdown handler returns its post, so Pi waits for the quit
// report, at most 500 ms; every other post is fired without waiting.
// Background child agents from pi-agent-fleet announce their count on Pi's
// event bus; each change is posted as children_running, using the context
// of the latest lifecycle event, so a settled session with working children
// shows as running.

type Ctx = {
  mode: string;
  cwd: string;
  sessionManager: { getSessionFile(): string | undefined; getSessionId(): string };
};
type Handler = (event: Record<string, unknown>, ctx: Ctx) => unknown;
type EventBus = { on(channel: string, handler: (data: unknown) => void): unknown };
type Pi = { on(event: string, handler: Handler): unknown; events?: EventBus };

const childrenChannel = "pi-agent-fleet:children-running";

const url = `http://${process.env.TCM_HOST || "127.0.0.1"}:${process.env.TCM_PORT || "7391"}/hook`;

function report(event: string, ctx: Ctx, extra: Record<string, unknown>): Promise<unknown> | undefined {
  try {
    if (ctx.mode !== "tui") return undefined;
    const body = JSON.stringify({
      agent: "pi",
      event,
      pid: process.pid,
      session_id: ctx.sessionManager.getSessionId(),
      transcript_path: ctx.sessionManager.getSessionFile(),
      cwd: ctx.cwd,
      ...extra,
    });
    return fetch(url, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body,
      signal: AbortSignal.timeout(500),
    }).catch(() => {});
  } catch {
    // tcm is optional; a failed report changes nothing in Pi.
    return undefined;
  }
}

export default function (pi: Pi): void {
  let lastCtx: Ctx | undefined;
  const on = (event: string, handler: Handler) =>
    pi.on(event, (e, ctx) => {
      lastCtx = ctx;
      return handler(e, ctx);
    });
  on("session_start", (_e, ctx) => void report("session_start", ctx, {}));
  on("agent_start", (_e, ctx) => void report("agent_start", ctx, {}));
  on("agent_settled", (e, ctx) => void report("agent_settled", ctx, { aborted: e.aborted }));
  on("ui_prompt_start", (_e, ctx) => void report("ui_prompt_start", ctx, {}));
  on("ui_prompt_end", (_e, ctx) => void report("ui_prompt_end", ctx, {}));
  on("session_shutdown", (e, ctx) => report("session_shutdown", ctx, { shutdown_reason: e.reason }));
  try {
    pi.events?.on(childrenChannel, (data) => {
      const running = (data as { running?: unknown } | null)?.running;
      if (!lastCtx || typeof running !== "number" || !Number.isInteger(running) || running < 0) return;
      void report("children_running", lastCtx, { children_running: running });
    });
  } catch {
    // A Pi without a usable event bus reports no child agents.
  }
}
