// tcm status reporter for the Pi coding agent. Interactive sessions post
// their lifecycle to tcm's /hook endpoint so the tcm sidebar shows live
// status, including waiting on a dialog. Each post is fired without being
// awaited, times out after 500 ms, and swallows every error: the extension
// never blocks, throws or prints.

type Ctx = {
  mode: string;
  cwd: string;
  sessionManager: { getSessionFile(): string | undefined; getSessionId(): string };
};
type Handler = (event: Record<string, unknown>, ctx: Ctx) => unknown;
type Pi = { on(event: string, handler: Handler): unknown };

const url = `http://${process.env.TCM_HOST || "127.0.0.1"}:${process.env.TCM_PORT || "7391"}/hook`;

function report(event: string, ctx: Ctx, extra: Record<string, unknown>): void {
  try {
    if (ctx.mode !== "tui") return;
    const body = JSON.stringify({
      agent: "pi",
      event,
      pid: process.pid,
      session_id: ctx.sessionManager.getSessionId(),
      transcript_path: ctx.sessionManager.getSessionFile(),
      cwd: ctx.cwd,
      tmux_pane: process.env.TMUX_PANE,
      ...extra,
    });
    fetch(url, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body,
      signal: AbortSignal.timeout(500),
    }).catch(() => {});
  } catch {
    // tcm is optional; a failed report changes nothing in Pi.
  }
}

export default function (pi: Pi): void {
  pi.on("session_start", (e, ctx) => report("session_start", ctx, { start_reason: e.reason }));
  pi.on("agent_start", (_e, ctx) => report("agent_start", ctx, {}));
  pi.on("agent_settled", (e, ctx) => report("agent_settled", ctx, { aborted: e.aborted }));
  pi.on("ui_prompt_start", (e, ctx) => report("ui_prompt_start", ctx, { prompt_kind: e.kind, prompt_title: e.title }));
  pi.on("ui_prompt_end", (e, ctx) => report("ui_prompt_end", ctx, { prompt_kind: e.kind, prompt_title: e.title }));
  pi.on("session_shutdown", (e, ctx) => report("session_shutdown", ctx, { shutdown_reason: e.reason }));
}
