/**
 * opensessions plugin for OpenCode
 *
 * Reports agent status to the opensessions server.
 *
 * Install:
 *   1. Copy this file to ~/.config/opencode/plugins/opensessions.ts
 *   2. Or add "opensessions-opencode" to your opencode.json plugins array
 *
 * Events mapped:
 *   session.idle   → idle
 *   session.status → running
 *   session.error  → error
 *   session.created → idle
 *   session.deleted → done
 */

const SERVER_URL = process.env.OPENSESSIONS_URL ?? "http://127.0.0.1:7391/event";
const EVENTS_FILE = process.env.OPENSESSIONS_EVENTS_FILE ?? "/tmp/opensessions-events.jsonl";

function getSession(): string {
  if (process.env.TMUX) {
    try {
      const result = Bun.spawnSync(["tmux", "display-message", "-p", "#S"], {
        stdout: "pipe", stderr: "pipe",
      });
      return result.stdout.toString().trim() || "unknown";
    } catch {
      return "unknown";
    }
  }
  if (process.env.ZELLIJ_SESSION_NAME) {
    return process.env.ZELLIJ_SESSION_NAME;
  }
  return "unknown";
}

async function postEvent(status: string, session?: string): Promise<void> {
  const sess = session ?? getSession();
  const payload = JSON.stringify({
    agent: "opencode",
    session: sess,
    status,
    ts: Date.now(),
  });

  try {
    await fetch(SERVER_URL, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: payload,
    });
  } catch {
    try {
      const { appendFileSync } = require("fs");
      appendFileSync(EVENTS_FILE, payload + "\n");
    } catch {}
  }
}

// Event type → opensessions status mapping
const EVENT_MAP: Record<string, string> = {
  "session.idle": "idle",
  "session.status": "running",
  "session.error": "error",
  "session.created": "idle",
  "session.deleted": "done",
  "session.compacted": "running",
};

// OpenCode plugin factory (matches @opencode-ai/plugin Plugin type)
export const OpensessionsPlugin = async (ctx: {
  project?: any;
  client?: any;
  $?: any;
  directory?: string;
  worktree?: string;
}) => {
  const session = getSession();

  // Report initial idle state
  await postEvent("idle", session);

  return {
    event: async ({ event }: { event: { type: string; properties?: any } }) => {
      const status = EVENT_MAP[event.type];
      if (status) {
        await postEvent(status, session);
      }
    },

    // Also hook into tool execution for real-time "running" feedback
    "tool.execute.before": async (_input: any, _output: any) => {
      await postEvent("running", session);
    },
  };
};

// Also export as default for compatibility with both loading patterns
export default OpensessionsPlugin;
