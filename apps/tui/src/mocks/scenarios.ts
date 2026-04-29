/**
 * Canonical mock scenarios.
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

// `NOW` is captured at module-load time — not a frozen literal — so relative
// timestamps in the activity zone (e.g. `2s`, `5s`, `30s`) reflect realistic
// recency in --mock mode. Within a single TUI session this is still
// deterministic enough for screenshots; only cross-session reproducibility is
// lost.
const NOW = Date.now();
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

const tcmLive = makeSession({
  name: "tcm",
  branch: "main",
  agents: [
    workingAgent({
      agent: "pi",
      session: "tcm",
      threadId: "15c80001-aaaa-aaaa-aaaa-000000000000",
      toolDescription: "ask_user",
    }),
    readyAgent({
      agent: "pi",
      session: "tcm",
      threadId: "10bc0001-aaaa-aaaa-aaaa-000000000000",
    }),
    readyAgent({ agent: "claude-code", session: "tcm" }),
    readyAgent({
      agent: "claude-code",
      session: "tcm",
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
    description: "5 sessions, tcm focused with 4 ready agents, no recent activity.",
    sessions: [aiEngineeringTemplate, piMono, tcmLive, claudeCodeSystem, theThemerReady],
    focusedSession: "tcm",
    currentSession: "tcm",
    paneFocused: true,
  },

  live: {
    name: "live",
    description: "Same dataset; tcm has a populated activity buffer including a multi-line skill prompt.",
    sessions: [
      aiEngineeringTemplate,
      piMono,
      {
        ...tcmLive,
        metadata: {
          status: { text: "ask_user", tone: "info", ts: NOW },
          progress: null,
          logs: [
            { message: "ask_user", source: "pi 15c8", tone: "info", ts: NOW - 1000 },
            { message: "Base directory for this skill: /Users/kyle/.local/share/skills/tcm-redesign", source: "cc 1859", tone: "neutral", ts: NOW - 5000 },
            { message: "ran  bun test (passed)", source: "cc 1859", tone: "success", ts: NOW - 30000 },
            { message: "awaiting input", source: "pi 10bc", tone: "info", ts: NOW - 60000 },
          ],
        },
      },
      claudeCodeSystem,
      theThemerReady,
    ],
    focusedSession: "tcm",
    currentSession: "tcm",
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
      tcmLive,
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
    sessions: [aiEngineeringTemplate, piMono, tcmLive, claudeCodeSystem, theThemerReady],
    focusedSession: "tcm",
    currentSession: "tcm",
    paneFocused: false,
  },

  // ── Activity-zone state mocks (one per canonical state in the spec) ──
  // See docs/simmer/activity-zone/result.md §States.

  "activity-empty": {
    name: "activity-empty",
    description: "tcm focused; no logs at all (Sparkline State 1 sub-case i — idle).",
    sessions: [
      aiEngineeringTemplate,
      piMono,
      {
        ...tcmLive,
        metadata: { status: null, progress: null, logs: [] },
      },
      claudeCodeSystem,
      theThemerReady,
    ],
    focusedSession: "tcm",
    currentSession: "tcm",
    paneFocused: true,
  },

  "activity-stale": {
    name: "activity-stale",
    description: "tcm focused; logs > 64 s old (Sparkline State 1 sub-case ii — wedged, shows ·Nm suffix).",
    sessions: [
      aiEngineeringTemplate,
      piMono,
      {
        ...tcmLive,
        metadata: {
          status: null,
          progress: null,
          logs: [
            { message: "Reading build.ts",       source: "pi db92", tone: "neutral", ts: NOW - 15 * 60_000 },
            { message: "Reading tsconfig.json",  source: "pi db92", tone: "neutral", ts: NOW - 15 * 60_000 - 4_000 },
            { message: "Reading package.json",   source: "pi db92", tone: "neutral", ts: NOW - 15 * 60_000 - 8_000 },
            { message: "Searching ActivityZone", source: "pi db92", tone: "neutral", ts: NOW - 15 * 60_000 - 12_000 },
          ],
        },
      },
      claudeCodeSystem,
      theThemerReady,
    ],
    focusedSession: "tcm",
    currentSession: "tcm",
    paneFocused: true,
  },

  "activity-single": {
    name: "activity-single",
    description: "tcm focused; single-source steady run — 6 reads from pi db92 within 60 s (State 2).",
    sessions: [
      aiEngineeringTemplate,
      piMono,
      {
        ...tcmLive,
        metadata: {
          status: null,
          progress: null,
          logs: [
            { message: "Reading tiers.ts",        source: "pi db92", tone: "neutral", ts: NOW - 2_000 },
            { message: "Searching ActivityZone",  source: "pi db92", tone: "neutral", ts: NOW - 12_000 },
            { message: "Reading scenarios.ts",    source: "pi db92", tone: "neutral", ts: NOW - 22_000 },
            { message: "Reading package.json",    source: "pi db92", tone: "neutral", ts: NOW - 32_000 },
            { message: "Reading tsconfig.json",   source: "pi db92", tone: "neutral", ts: NOW - 44_000 },
            { message: "Reading build.ts",        source: "pi db92", tone: "neutral", ts: NOW - 56_000 },
          ],
        },
      },
      claudeCodeSystem,
      theThemerReady,
    ],
    focusedSession: "tcm",
    currentSession: "tcm",
    paneFocused: true,
  },

  "activity-multi": {
    name: "activity-multi",
    description: "tcm focused; pi/cc multi-source interleave — chip mode throughout (State 3).",
    sessions: [
      aiEngineeringTemplate,
      piMono,
      {
        ...tcmLive,
        metadata: {
          status: null,
          progress: null,
          logs: [
            { message: "Reading build.ts",       source: "pi db92", tone: "neutral", ts: NOW - 4_000 },
            { message: "Reading types.ts",       source: "cc 1859", tone: "neutral", ts: NOW - 11_000 },
            { message: "Reading scenarios.ts",   source: "pi db92", tone: "neutral", ts: NOW - 18_000 },
            { message: "Editing index.tsx",      source: "cc 1859", tone: "neutral", ts: NOW - 25_000 },
            { message: "Reading vocab.ts",       source: "pi db92", tone: "neutral", ts: NOW - 33_000 },
            { message: "Reading tiers.ts",       source: "cc 1859", tone: "neutral", ts: NOW - 41_000 },
          ],
        },
      },
      claudeCodeSystem,
      theThemerReady,
    ],
    focusedSession: "tcm",
    currentSession: "tcm",
    paneFocused: true,
  },

  "activity-errors": {
    name: "activity-errors",
    description: "tcm focused; error-heavy cascade — 4 failed rows + 1 surviving read (State 4).",
    sessions: [
      aiEngineeringTemplate,
      piMono,
      {
        ...tcmLive,
        metadata: {
          status: null,
          progress: null,
          logs: [
            { message: "Editing src/index.tsx (failed)", source: "pi 15c8", tone: "error",   ts: NOW - 2_000 },
            { message: "Editing types.ts (failed)",      source: "pi 15c8", tone: "error",   ts: NOW - 8_000 },
            { message: "Reading tsconfig.json",          source: "pi 15c8", tone: "neutral", ts: NOW - 18_000 },
            { message: "Editing vocab.ts (failed)",      source: "pi 15c8", tone: "error",   ts: NOW - 28_000 },
            { message: "Editing tiers.ts (failed)",      source: "pi 15c8", tone: "error",   ts: NOW - 40_000 },
          ],
        },
      },
      claudeCodeSystem,
      theThemerReady,
    ],
    focusedSession: "tcm",
    currentSession: "tcm",
    paneFocused: true,
  },

  "activity-bell": {
    name: "activity-bell",
    description: "tcm focused; system-tag [bell] interleaved with pi reads (Rule 0 system-tag precedence).",
    sessions: [
      aiEngineeringTemplate,
      piMono,
      {
        ...tcmLive,
        metadata: {
          status: null,
          progress: null,
          logs: [
            { message: "Reading build.ts",         source: "pi db92", tone: "neutral", ts: NOW - 3_000 },
            { message: "awaiting confirmation",    source: "[bell]",  tone: "warn",    ts: NOW - 8_000 },
            { message: "Reading tsconfig.json",    source: "pi db92", tone: "neutral", ts: NOW - 14_000 },
            { message: "Reading package.json",     source: "pi db92", tone: "neutral", ts: NOW - 22_000 },
            { message: "Reading scenarios.ts",     source: "pi db92", tone: "neutral", ts: NOW - 32_000 },
          ],
        },
      },
      claudeCodeSystem,
      theThemerReady,
    ],
    focusedSession: "tcm",
    currentSession: "tcm",
    paneFocused: true,
  },
};

export type MockScenarioName = keyof typeof MOCK_SCENARIOS;

export function getScenario(name: string): MockScenario | null {
  return MOCK_SCENARIOS[name] ?? null;
}

export function listScenarios(): string[] {
  return Object.keys(MOCK_SCENARIOS);
}
