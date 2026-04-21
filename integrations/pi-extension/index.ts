/**
 * opensessions pi extension.
 *
 * Pushes pi lifecycle events to the local opensessions server so pi sessions
 * show up in the sidebar alongside Claude Code. All requests are strictly
 * fire-and-forget: hook failures must never block the agent.
 *
 * Wire format is documented in `CONTRACTS.md` (Built-In Watchers > Pi).
 */

import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";

// --- Transport ---

const DEFAULT_PORT = 7391;
const DEFAULT_HOST = "127.0.0.1";
// Mirror scripts/hook.sh behavior: bail quickly if the server is gone so the
// agent does not stall waiting for a reply.
const CONNECT_TIMEOUT_MS = 1_000;
const REQUEST_TIMEOUT_MS = 2_000;

interface PiHookBody {
  agent: "pi";
  event: string;
  session_id: string;
  cwd: string;
  session_name?: string;
  tool_name?: string;
  tool_input?: Record<string, unknown>;
  tool_is_error?: boolean;
  stop_reason?: "stop" | "length" | "toolUse" | "error" | "aborted";
  error_message?: string;
  shutdown_reason?: "quit" | "reload" | "new" | "resume" | "fork";
}

function endpoint(): string {
  const port = process.env.OPENSESSIONS_PORT ?? DEFAULT_PORT;
  const host = process.env.OPENSESSIONS_HOST ?? DEFAULT_HOST;
  return `http://${host}:${port}/hook`;
}

/** Fire a hook body at the opensessions server without awaiting. */
function post(body: PiHookBody): void {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  // Note: `signal` covers both connect and response windows — fetch does not
  // expose a separate connect timeout, but the request timeout is strict.
  void fetch(endpoint(), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    signal: controller.signal,
    keepalive: true,
  })
    .catch(() => {
      // Hooks must never block the agent. Swallow any error.
    })
    .finally(() => {
      clearTimeout(timer);
    });
  // CONNECT_TIMEOUT_MS is a budget hint for future AbortSignal.any() support
  // once widely available; fetch's single timeout already bounds the request.
  void CONNECT_TIMEOUT_MS;
}

// --- Extension ---

export default function (pi: ExtensionAPI) {
  // Track the most recent assistant stopReason so agent_end can report it.
  // Pi's `agent_end` event does not carry stopReason directly — we sniff it
  // from the trailing assistant message_end in the same turn.
  let lastStopReason: PiHookBody["stop_reason"] | undefined;
  let lastErrorMessage: string | undefined;

  function sessionId(ctx: { sessionManager: { getSessionId(): string | undefined } }): string | undefined {
    try { return ctx.sessionManager.getSessionId(); } catch { return undefined; }
  }

  // --- Lifecycle ---

  pi.on("session_start", (_event, ctx) => {
    const id = sessionId(ctx);
    if (!id) return;
    post({
      agent: "pi",
      event: "session_start",
      session_id: id,
      cwd: ctx.cwd,
      session_name: pi.getSessionName(),
    });
  });

  pi.on("agent_start", (_event, ctx) => {
    const id = sessionId(ctx);
    if (!id) return;
    // Reset turn-scoped state.
    lastStopReason = undefined;
    lastErrorMessage = undefined;
    post({ agent: "pi", event: "agent_start", session_id: id, cwd: ctx.cwd });
  });

  pi.on("message_end", (event, _ctx) => {
    // Capture the assistant stopReason so we can forward it on agent_end.
    // Pi's AssistantMessage carries stopReason + optional errorMessage.
    const message = (event as { message?: { role?: string; stopReason?: PiHookBody["stop_reason"]; errorMessage?: string } }).message;
    if (message?.role === "assistant") {
      lastStopReason = message.stopReason;
      lastErrorMessage = message.errorMessage;
    }
  });

  pi.on("agent_end", (_event, ctx) => {
    const id = sessionId(ctx);
    if (!id) return;
    post({
      agent: "pi",
      event: "agent_end",
      session_id: id,
      cwd: ctx.cwd,
      stop_reason: lastStopReason,
      error_message: lastErrorMessage,
    });
  });

  pi.on("tool_execution_start", (event, ctx) => {
    const id = sessionId(ctx);
    if (!id) return;
    post({
      agent: "pi",
      event: "tool_execution_start",
      session_id: id,
      cwd: ctx.cwd,
      tool_name: event.toolName,
      tool_input: event.args as Record<string, unknown>,
    });
  });

  pi.on("tool_execution_end", (event, ctx) => {
    const id = sessionId(ctx);
    if (!id) return;
    post({
      agent: "pi",
      event: "tool_execution_end",
      session_id: id,
      cwd: ctx.cwd,
      tool_name: event.toolName,
      tool_is_error: event.isError,
    });
  });

  pi.on("session_shutdown", (event, ctx) => {
    const id = sessionId(ctx);
    if (!id) return;
    const shutdownReason = (event as { reason?: PiHookBody["shutdown_reason"] }).reason;
    post({
      agent: "pi",
      event: "session_shutdown",
      session_id: id,
      cwd: ctx.cwd,
      shutdown_reason: shutdownReason,
    });
  });
}
