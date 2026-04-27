/**
 * Canonical mock scenarios.
 *
 * Source of truth: docs/design/04-mockups/02-canonical.md.
 *
 * These datasets are used by the `--mock <scenario>` flag to render the
 * panel without a running server. Useful for design iteration, screenshot
 * generation, and OpenTUI integration smoke tests.
 *
 * Each scenario is a complete `ServerState`-shaped fixture. The TUI's
 * normal websocket path is bypassed when `--mock` is set; instead the
 * fixture is fed directly into the render store and stays static.
 */

import type { SessionData } from "@tcm/runtime";
import type { AgentEvent } from "@tcm/runtime";

export interface MockScenario {
  name: string;
  description: string;
  sessions: SessionData[];
  focusedSession: string;
  currentSession: string;
  paneFocused: boolean;
}

const NOW = 1735000000000; // stable timestamp; deterministic for screenshots
const HOUR_AGO = NOW - 60 * 60 * 1000;
const FOUR_MIN_AGO = NOW - 4 * 60 * 1000;

// ── Agent factories ──

function readyAgent(opts: { agent: string; session: string; threadId?: string; threadName?: string; unseen?: boolean }): AgentEvent {
  return {
    agent: opts.agent,
    session: opts.session,
    status: "done",
    liveness: "alive",
    ts: HOUR_AGO,
    threadId: opts.threadId,
    threadName: opts.threadName,
    unseen: opts.unseen ?? false,
  };
}

function workingAgent(opts: { agent: string; session: string; threadId?: string; threadName?: string; toolDescription?: string }): AgentEvent {
  return {
    agent: opts.agent,
    session: opts.session,
    status: "running",
    liveness: "alive",
    ts: NOW,
    threadId: opts.threadId,
    threadName: opts.threadName,
    toolDescription: opts.toolDescription,
  };
}

function erroredAgent(opts: { agent: string; session: string; threadId?: string; threadName?: string }): AgentEvent {
  return {
    agent: opts.agent,
    session: opts.session,
    status: "error",
    liveness: "alive",
    ts: NOW,
    threadId: opts.threadId,
    threadName: opts.threadName,
  };
}

function stoppedAgent(opts: { agent: string; session: string; threadId?: string }): AgentEvent {
  return {
    agent: opts.agent,
    session: opts.session,
    status: "done",
    liveness: "exited",
    ts: FOUR_MIN_AGO,
    threadId: opts.threadId,
  };
}

// ── Session factories ──

function makeSession(opts: {
  name: string;
  branch?: string;
  dir?: string;
  agents: AgentEvent[];
  unseen?: boolean;
  metadata?: SessionData["metadata"];
  worstAgent?: AgentEvent;
}): SessionData {
  const agents = opts.agents;
  // The session's agentState is a rollup of the worst (most-attention-needing)
  // agent state, mirroring how the server picks it. Caller can override.
  const agentState = opts.worstAgent ?? agents.find((a) => a.status === "error")
    ?? agents.find((a) => a.status === "running")
    ?? agents.find((a) => a.status === "waiting")
    ?? agents[0]
    ?? null;

  return {
    name: opts.name,
    createdAt: HOUR_AGO,
    dir: opts.dir ?? `/Users/kyle/Code/${opts.name}`,
    branch: opts.branch ?? "",
    dirty: false,
    isWorktree: false,
    unseen: opts.unseen ?? false,
    panes: 1,
    windows: 1,
    uptime: "1h",
    agentState,
    agents,
    eventTimestamps: [],
    metadata: opts.metadata,
  };
}

// ── Reusable session pieces ──

const aiEngineeringTemplate = makeSession({
  name: "ai-engineering-template",
  branch: "main",
  agents: [readyAgent({ agent: "generic", session: "ai-engineering-template" })],
});

const piMono = makeSession({
  name: "pi-mono",
  branch: "main",
  agents: [
    readyAgent({ agent: "pi", session: "pi-mono", threadId: "20cd0001-aaaa-aaaa-aaaa-000000000000" }),
    readyAgent({ agent: "pi", session: "pi-mono", threadId: "20de0001-aaaa-aaaa-aaaa-000000000000" }),
  ],
});

const opensessionsLive = makeSession({
  name: "opensessions",
  branch: "main",
  agents: [
    workingAgent({
      agent: "pi",
      session: "opensessions",
      threadId: "15c80001-aaaa-aaaa-aaaa-000000000000",
      toolDescription: "ask_user",
    }),
    readyAgent({
      agent: "pi",
      session: "opensessions",
      threadId: "10bc0001-aaaa-aaaa-aaaa-000000000000",
    }),
    readyAgent({ agent: "claude-code", session: "opensessions" }),
    readyAgent({
      agent: "claude-code",
      session: "opensessions",
      threadId: "18590001-aaaa-aaaa-aaaa-000000000000",
    }),
  ],
});

const claudeCodeSystem = makeSession({
  name: "claude-code-system…",
  agents: [],
});

const theThemerReady = makeSession({
  name: "the-themer",
  agents: [readyAgent({ agent: "generic", session: "the-themer" })],
});

const theThemerErrored = makeSession({
  name: "the-themer",
  agents: [erroredAgent({ agent: "generic", session: "the-themer" })],
});

// ── Scenarios ──

export const MOCK_SCENARIOS: Record<string, MockScenario> = {
  quiet: {
    name: "quiet",
    description: "5 sessions, opensessions focused with 4 ready agents, no recent activity.",
    sessions: [aiEngineeringTemplate, piMono, opensessionsLive, claudeCodeSystem, theThemerReady],
    focusedSession: "opensessions",
    currentSession: "opensessions",
    paneFocused: true,
  },

  live: {
    name: "live",
    description: "Same dataset; opensessions has a populated activity buffer including a multi-line skill prompt.",
    sessions: [
      aiEngineeringTemplate,
      piMono,
      {
        ...opensessionsLive,
        metadata: {
          status: { text: "ask_user", tone: "info", ts: NOW },
          progress: null,
          logs: [
            { message: "ask_user", source: "pi 15c8", tone: "info", ts: NOW - 1000 },
            { message: "Base directory for this skill: /Users/kyle/.local/share/skills/opensessions-redesign", source: "cc 1859", tone: "neutral", ts: NOW - 5000 },
            { message: "ran  bun test (passed)", source: "cc 1859", tone: "success", ts: NOW - 30000 },
            { message: "awaiting input", source: "pi 10bc", tone: "info", ts: NOW - 60000 },
          ],
        },
      },
      claudeCodeSystem,
      theThemerReady,
    ],
    focusedSession: "opensessions",
    currentSession: "opensessions",
    paneFocused: true,
  },

  errored: {
    name: "errored",
    description: "pi-mono is focused; the-themer has an errored generic agent.",
    sessions: [
      aiEngineeringTemplate,
      {
        ...piMono,
        metadata: {
          status: null,
          progress: null,
          logs: [
            { message: "saw new file", source: "pi 20cd", tone: "neutral", ts: NOW - 5000 },
            { message: "ran  pytest (passed)", source: "pi 20de", tone: "success", ts: NOW - 30000 },
          ],
        },
      },
      opensessionsLive,
      claudeCodeSystem,
      theThemerErrored,
    ],
    focusedSession: "pi-mono",
    currentSession: "pi-mono",
    paneFocused: true,
  },

  unfocused: {
    name: "unfocused",
    description: "Same as quiet but the panel pane is unfocused (sleep state).",
    sessions: [aiEngineeringTemplate, piMono, opensessionsLive, claudeCodeSystem, theThemerReady],
    focusedSession: "opensessions",
    currentSession: "opensessions",
    paneFocused: false,
  },
};

export type MockScenarioName = keyof typeof MOCK_SCENARIOS;

export function getScenario(name: string): MockScenario | null {
  return MOCK_SCENARIOS[name] ?? null;
}

export function listScenarios(): string[] {
  return Object.keys(MOCK_SCENARIOS);
}
