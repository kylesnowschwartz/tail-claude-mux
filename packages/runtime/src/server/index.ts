import { existsSync, readFileSync, unlinkSync, writeFileSync, appendFileSync, watch, type FSWatcher } from "fs";
import { join } from "path";
import { homedir } from "os";
import type { MuxProvider } from "../contracts/mux";
import { isFullSidebarCapable, isBatchCapable } from "../contracts/mux";
import type { AgentEvent } from "../contracts/agent";
import type { AgentWatcher, AgentWatcherContext } from "../contracts/agent-watcher";
import { isHookReceiver } from "../contracts/agent-watcher";
import { AgentTracker } from "../agents/tracker";
import { SessionOrder } from "./session-order";
import { SessionMetadataStore } from "./metadata-store";
import { loadConfig, saveConfig } from "../config";
import { resolveTheme, loadExternalTheme, type PartialTheme } from "../themes";
import { syncTmuxHeaderOptions } from "./tmux-header-sync";
import {
  clampSidebarWidth,
  computeMinSidebarWidth,
  ABSOLUTE_MIN_SIDEBAR_WIDTH,
  SAVE_DEBOUNCE_MS,
} from "./sidebar-width-sync";
import {
  type ServerState,
  type SessionData,
  type ClientCommand,
  type FocusUpdate,
  SERVER_PORT,
  SERVER_HOST,
  PID_FILE,
  SERVER_IDLE_TIMEOUT_MS,
  STUCK_RUNNING_TIMEOUT_MS,
} from "../shared";

// --- Debug logger ---

const DEBUG_LOG = "/tmp/tcm-debug.log";
function log(category: string, msg: string, data?: Record<string, unknown>) {
  const ts = new Date().toISOString().slice(11, 23);
  const extra = data ? " " + JSON.stringify(data) : "";
  const line = `[${ts}] [${category}] ${msg}${extra}\n`;
  try { appendFileSync(DEBUG_LOG, line); } catch {}
}

// --- Shell helper (for git commands only) ---

function shell(cmd: string[]): string {
  try {
    const result = Bun.spawnSync(cmd, { stdout: "pipe", stderr: "pipe" });
    return result.stdout.toString().trim();
  } catch {
    return "";
  }
}

// --- Tmux hook install verifier ---
//
// setupHooks() is fire-and-forget today: each tmux command silently swallows
// errors. After install we ask tmux directly which hooks are populated and
// log any expected hook that came back empty. This catches both genuine
// install failures and "a previous uninstall.sh forgot to clear me" drift.
const EXPECTED_TMUX_GLOBAL_HOOKS = [
  "client-session-changed",
  "session-created",
  "session-closed",
  "after-select-window",
  "after-new-window",
  "client-resized",
  "after-kill-pane",
] as const;
const EXPECTED_TMUX_WINDOW_HOOKS = ["pane-exited", "pane-focus-in"] as const;

function verifyTmuxHooksInstalled(): void {
  // Hooks installed by us all run our own server URL via curl, so a simple
  // substring search on /127\.0\.0\.1:<PORT>/ is a robust uniqueness check.
  const port = String(SERVER_PORT);
  const installed = new Set<string>();
  const scan = (flags: string[]) => {
    const out = shell(["tmux", "show-hooks", ...flags]);
    for (const line of out.split("\n")) {
      // Format: "<hook-name>[<idx>] <command>" when populated, bare "<hook-name>" when empty.
      const m = line.match(/^([a-z-]+)\[\d+\]\s+(.*)$/);
      if (!m) continue;
      const [, name, body] = m;
      if (name && body && body.includes(`:${port}/`)) installed.add(name);
    }
  };
  scan(["-g"]);
  scan(["-gw"]);

  const missing: string[] = [];
  for (const h of EXPECTED_TMUX_GLOBAL_HOOKS) if (!installed.has(h)) missing.push(h);
  for (const h of EXPECTED_TMUX_WINDOW_HOOKS) if (!installed.has(h)) missing.push(`${h} (-gw)`);

  if (missing.length === 0) {
    log("bootstrap", "tmux hooks verified", { count: installed.size });
  } else {
    log("bootstrap", "tmux hooks MISSING", { missing, installed: [...installed] });
  }
}

/** Match a comm string against a pattern as a whole word.
 *  "claude" matches "claude", "/usr/bin/claude", "claude-code"
 *  but NOT "tail-claude", "pip" (vs "pi"), or "claude.fork".
 *  The pattern must:
 *    - start at the beginning of comm OR be preceded by a path separator (/)
 *    - end at the end of comm OR be followed by a hyphen (-)
 *  The hyphen-suffix exception preserves matches like "claude" → "claude-code". */
export function commMatches(comm: string, pat: string): boolean {
  const idx = comm.indexOf(pat);
  if (idx < 0) return false;
  if (idx > 0 && comm[idx - 1] !== "/") return false;
  const tail = comm[idx + pat.length];
  if (tail !== undefined && tail !== "-") return false;
  return true;
}

/** Like `shell()` but throws on nonzero exit. Use when "command failed" must
 *  be observable — e.g. so a caller's try/catch can avoid advancing cached
 *  state past a TOCTOU failure. The plain `shell()` helper masks failures
 *  (returns empty string on both "command succeeded with no output" and
 *  "command failed") which is fine for git probes but wrong for sync paths
 *  that compute idempotency diffs against an in-memory cache. */
function shellStatus(cmd: string[]): string {
  const result = Bun.spawnSync(cmd, { stdout: "pipe", stderr: "pipe" });
  if (!result.success) {
    const stderr = result.stderr.toString().trim();
    throw new Error(`${cmd.join(" ")} exited ${result.exitCode ?? "?"}: ${stderr || "<no stderr>"}`);
  }
  return result.stdout.toString().trim();
}

// --- Git helpers ---

interface GitInfo {
  branch: string;
  dirty: boolean;
  isWorktree: boolean;
}

const gitInfoCache = new Map<string, { info: GitInfo; ts: number }>();
const GIT_CACHE_TTL_MS = 5000;

function getGitInfo(dir: string): GitInfo {
  if (!dir) return { branch: "", dirty: false, isWorktree: false };

  const cached = gitInfoCache.get(dir);
  if (cached && Date.now() - cached.ts < GIT_CACHE_TTL_MS) return cached.info;

  const out = shell([
    "sh", "-c",
    `cd "${dir}" 2>/dev/null && git rev-parse --abbrev-ref HEAD --git-dir 2>/dev/null && echo "---" && git status --porcelain 2>/dev/null`,
  ]);
  if (!out) return { branch: "", dirty: false, isWorktree: false };
  const sepIdx = out.indexOf("---");
  const headerPart = sepIdx >= 0 ? out.slice(0, sepIdx).trim() : out.trim();
  const statusPart = sepIdx >= 0 ? out.slice(sepIdx + 3).trim() : "";
  const lines = headerPart.split("\n");
  const branch = lines[0] ?? "";
  const gitDir = lines[1] ?? "";
  const info: GitInfo = {
    branch,
    dirty: statusPart.length > 0,
    isWorktree: gitDir.includes("/worktrees/"),
  };
  gitInfoCache.set(dir, { info, ts: Date.now() });
  return info;
}

function invalidateGitCache(dir?: string) {
  if (dir) gitInfoCache.delete(dir);
  else gitInfoCache.clear();
}


// --- Git HEAD file watchers ---

const gitHeadWatchers = new Map<string, FSWatcher>();

function resolveGitHeadPath(dir: string): string | null {
  if (!dir) return null;
  const gitDir = shell(["git", "-C", dir, "rev-parse", "--git-dir"]);
  if (!gitDir) return null;
  const absGitDir = gitDir.startsWith("/") ? gitDir : join(dir, gitDir);
  const headPath = join(absGitDir, "HEAD");
  return existsSync(headPath) ? headPath : null;
}

let debounceTimer: ReturnType<typeof setTimeout> | null = null;

function onGitHeadChange(broadcastFn: () => void) {
  if (debounceTimer) return;
  debounceTimer = setTimeout(() => {
    debounceTimer = null;
    invalidateGitCache();
    broadcastFn();
  }, 200);
}

function syncGitWatchers(sessions: SessionData[], broadcastFn: () => void) {
  const currentDirs = new Set<string>();
  for (const s of sessions) {
    if (s.dir) currentDirs.add(s.dir);
  }

  for (const [dir, watcher] of gitHeadWatchers) {
    if (!currentDirs.has(dir)) {
      watcher.close();
      gitHeadWatchers.delete(dir);
    }
  }

  for (const dir of currentDirs) {
    if (gitHeadWatchers.has(dir)) continue;
    const headPath = resolveGitHeadPath(dir);
    if (!headPath) continue;
    try {
      const watcher = watch(headPath, () => onGitHeadChange(broadcastFn));
      gitHeadWatchers.set(dir, watcher);
    } catch { /* ignore */ }
  }
}

// --- Server startup ---

export function startServer(mux: MuxProvider, extraProviders?: MuxProvider[], watchers?: AgentWatcher[]): void {
  const allProviders = [mux, ...(extraProviders ?? [])];
  const allWatchers = watchers ?? [];
  const tracker = new AgentTracker();
  const metadataStore = new SessionMetadataStore();
  const home = process.env.HOME ?? process.env.USERPROFILE ?? "";
  const sessionOrderPath = join(home, ".config", "tcm", "session-order.json");
  const sessionOrder = new SessionOrder(sessionOrderPath);

  // Clear previous log on server start
  try { writeFileSync(DEBUG_LOG, ""); } catch {}
  log("server", "starting", { providers: allProviders.map((p) => p.name) });

  // Load initial theme from config
  const config = loadConfig();
  let currentTheme: string | undefined = typeof config.theme === "string" ? config.theme : undefined;

  // External theme override (typically written by the-themer's tcm adapter).
  // When present, takes precedence over `currentTheme` from config.json. The watcher
  // re-reads on filesystem change and triggers a broadcast so the panel + tmux header
  // repaint within a frame of the user swapping their terminal theme.
  const externalThemePath = join(homedir(), ".config", "tcm", "active-theme.json");
  let externalTheme: PartialTheme | null = null;
  let externalThemeWatcher: FSWatcher | null = null;
  let configuredWidth = clampSidebarWidth(config.sidebarWidth ?? 26);
  let sidebarPosition: "left" | "right" = config.sidebarPosition ?? "left";
  let sidebarVisible = false;

  // The sidebar launcher lives with the TUI app, not the tmux integration layer.
  const scriptsDir = (() => {
    const envDir = process.env.TCM_DIR;
    if (envDir) return join(envDir, "apps", "tui", "scripts");
    // Fallback: relative to this file
    return join(import.meta.dir, "..", "..", "..", "..", "apps", "tui", "scripts");
  })();

  log("server", "config loaded", {
    sidebarWidth: configuredWidth, sidebarPosition, scriptsDir,
    theme: currentTheme, configKeys: Object.keys(config),
  });

  // --- External theme loader + watcher ---
  function reloadExternalTheme(reason: string): boolean {
    let next: PartialTheme | null = null;
    if (existsSync(externalThemePath)) {
      try {
        const text = readFileSync(externalThemePath, "utf-8");
        next = loadExternalTheme(text);
        if (!next) {
          log("server", "external theme rejected", { path: externalThemePath, reason });
        }
      } catch (err) {
        log("server", "external theme read failed", { error: String(err), reason });
        next = null;
      }
    }
    const changed = JSON.stringify(externalTheme) !== JSON.stringify(next);
    externalTheme = next;
    if (changed) {
      log("server", "external theme applied", {
        reason,
        name: next?.name ?? null,
        variant: next?.variant ?? null,
        paletteTokens: next?.palette ? Object.keys(next.palette).length : 0,
      });
    }
    return changed;
  }

  reloadExternalTheme("startup");

  // Watch the directory rather than the file so atomic writes (rename trick used
  // by most editors and `the-themer`'s symlink-then-replace flow) still trigger.
  try {
    const watchDir = join(homedir(), ".config", "tcm");
    if (existsSync(watchDir)) {
      externalThemeWatcher = watch(watchDir, (_event, filename) => {
        if (filename !== "active-theme.json") return;
        if (reloadExternalTheme("file-change")) broadcastState();
      });
    }
  } catch (err) {
    log("server", "external theme watch failed", { error: String(err) });
  }

  /** Resolve the theme value passed to broadcasts. External theme wins; falls back
   *  to the configured builtin name. */
  function effectiveThemeConfig(): string | PartialTheme | undefined {
    return externalTheme ?? currentTheme;
  }

  /** Resolve the human-readable theme name. Used by the tmux header sync to
   *  surface the active theme label — the broadcast state ships the full
   *  PartialTheme via effectiveThemeConfig() so panel clients can resolve a
   *  palette without a builtin-name lookup. */
  function effectiveThemeName(): string | undefined {
    return externalTheme?.name ?? currentTheme;
  }

  // Read @tcm-header gate once. Toggling at runtime requires a server
  // restart — same cost shape as other @tcm-* options read at TPM init.
  const headerEnabled = (() => {
    const raw = shell(["tmux", "show-option", "-gqv", "@tcm-header"]);
    return raw.trim() === "on";
  })();
  log("server", "tmux header", { enabled: headerEnabled });

  function tmuxHeaderShell(args: string[]): string {
    // shellStatus throws on nonzero exit. The try/catch in syncTmuxHeaderOptions
    // catches it and exits without advancing lastWindows/lastPalette, so the
    // failed write will be retried on the next broadcast.
    return shellStatus(["tmux", ...args]);
  }
  function tmuxHeaderLog(msg: string, data?: Record<string, unknown>): void {
    log("tmux-header", msg, data);
  }

  // Bootstrap active sessions
  const currentSession = mux.getCurrentSession();
  if (currentSession) {
    tracker.setActiveSessions([currentSession]);
  }

  // --- Agent watcher context ---

  let watcherBroadcastTimer: ReturnType<typeof setTimeout> | null = null;

  function debouncedBroadcast() {
    if (watcherBroadcastTimer) return;
    watcherBroadcastTimer = setTimeout(() => {
      watcherBroadcastTimer = null;
      broadcastState();
    }, 200);
  }

  // Cache for dir→session resolution (rebuilt per scan cycle)
  let dirSessionCache: Map<string, string> | null = null;
  let dirSessionCacheTs = 0;
  const DIR_CACHE_TTL = 5000;

  function getDirSessionMap(): Map<string, string> {
    const now = Date.now();
    if (dirSessionCache && now - dirSessionCacheTs < DIR_CACHE_TTL) return dirSessionCache;
    const map = new Map<string, string>();
    for (const p of allProviders) {
      for (const s of p.listSessions()) {
        if (s.dir) map.set(s.dir, s.name);
      }
    }
    dirSessionCache = map;
    dirSessionCacheTs = now;
    return map;
  }

  // ---- Activity log producer ----
  //
  // Watchers emit AgentEvents (status / tool / thread name) that are
  // routed through `watcherCtx.emit` below. We synthesize human-readable
  // log entries from those events and push them into the metadata store,
  // which the TUI's activity zone reads from.
  //
  // Per-thread state lets us diff each new event against the last one we
  // saw for that thread, so we only emit log entries on real transitions
  // (avoiding floods on heartbeat-style re-emits).
  type AgentSnapshot = { tool?: string; thread?: string; status?: string };
  const lastSeenByThread = new Map<string, AgentSnapshot>();

  function agentCode(agent: string): string {
    switch (agent) {
      case "claude-code": return "cc";
      case "pi": return "pi";
      case "codex": return "cd";
      case "amp": return "ap";
      default: return agent.slice(0, 2);
    }
  }

  function shortThreadIdSuffix(id?: string): string {
    if (!id) return "";
    return id.length <= 4 ? id : id.slice(-4);
  }

  function deriveLogEntries(event: AgentEvent): Array<{ message: string; tone?: import("../shared").MetadataTone; source?: string }> {
    if (!event.threadId) return [];
    const last = lastSeenByThread.get(event.threadId) ?? {};
    const source = `${agentCode(event.agent)} ${shortThreadIdSuffix(event.threadId)}`.trim();
    const out: Array<{ message: string; tone?: import("../shared").MetadataTone; source?: string }> = [];

    // Emit order: thread name (least recent) → tool (mid) → status transition
    // (most recent), so the freshest visible entry reflects the latest signal.
    if (event.threadName && event.threadName !== last.thread) {
      out.push({ source, message: event.threadName, tone: "neutral" });
    }
    if (event.toolDescription && event.toolDescription !== last.tool) {
      out.push({ source, message: event.toolDescription, tone: "info" });
    }
    if (event.status !== last.status) {
      if (event.status === "error") out.push({ source, message: "errored", tone: "error" });
      else if (event.status === "waiting") out.push({ source, message: "awaiting input", tone: "info" });
      else if (event.status === "interrupted") out.push({ source, message: "interrupted", tone: "warn" });
      // running, idle, done are intentionally not surfaced as discrete entries:
      // running is implied by tool descriptions; idle/done are too noisy.
    }

    lastSeenByThread.set(event.threadId, {
      tool: event.toolDescription,
      thread: event.threadName,
      status: event.status,
    });
    return out;
  }

  const watcherCtx: AgentWatcherContext = {
    resolveSession(projectDir: string): string | null {
      const map = getDirSessionMap();
      // Direct path match
      const direct = map.get(projectDir);
      if (direct) return direct;
      // Substring match (parent/child directories)
      for (const [dir, name] of map) {
        if (projectDir.startsWith(dir + "/") || dir.startsWith(projectDir + "/")) return name;
      }
      // Encoded match: the watcher couldn't decode the path unambiguously,
      // so try encoding each session dir and comparing against the encoded form.
      // Claude Code encodes /, ., and _ as - in project directory names.
      if (projectDir.startsWith("__encoded__:")) {
        const encoded = projectDir.slice("__encoded__:".length);
        for (const [dir, name] of map) {
          if (dir.replace(/[/._]/g, "-") === encoded) return name;
        }
      }
      return null;
    },
    emit(event: AgentEvent) {
      log("agent-emit", event.agent, { session: event.session, status: event.status, threadId: event.threadId?.slice(0, 8) });
      // Always update lastSeenByThread (so post-seed diffs are correct), but
      // only push log entries once initial seeding is complete — otherwise
      // every cold-start reconstruction would flood the buffer.
      const entries = deriveLogEntries(event);
      if (watchersSeeded) {
        for (const entry of entries) {
          metadataStore.appendLog(event.session, entry);
        }
      }
      tracker.applyEvent(event, { seed: !watchersSeeded });
      debouncedBroadcast();
    },
  };

  // Flag to track when initial watcher seeding is complete
  let watchersSeeded = false;
  setTimeout(() => {
    watchersSeeded = true;
    // Re-apply focus for the current session to clear seed-unseen flags
    // (handleFocus already ran before seed events arrived)
    const current = getCurrentSession();
    if (current && tracker.handleFocus(current)) {
      broadcastState();
    }
  }, 3000);

  let focusedSession: string | null = null;
  let lastState: ServerState | null = null;
  let clientCount = 0;
  let idleTimer: ReturnType<typeof setTimeout> | null = null;
  const clientTtys = new WeakMap<object, string>();
  const clientSessionNames = new WeakMap<object, string>();
  const sessionProviders = new Map<string, MuxProvider>();
  // Map session name → client TTY (from hook context, for multi-client setups)
  const clientTtyBySession = new Map<string, string>();

  function getCurrentSession(): string | null {
    // Try all providers until one returns a session
    for (const p of allProviders) {
      const result = p.getCurrentSession();
      if (result) {
        log("getCurrentSession", "result", { result, provider: p.name });
        return result;
      }
    }
    log("getCurrentSession", "no provider returned a session");
    return null;
  }

  function computeState(): ServerState {
    // Merge sessions from all providers
    const allMuxSessions: (import("../contracts/mux").MuxSessionInfo & { provider: MuxProvider })[] = [];
    for (const p of allProviders) {
      for (const s of p.listSessions()) {
        allMuxSessions.push({ ...s, provider: p });
      }
    }
    allMuxSessions.sort((a, b) => {
      if (a.createdAt !== b.createdAt) return a.createdAt - b.createdAt;
      return a.name.localeCompare(b.name);
    });

    const currentSession = getCurrentSession();

    // Sync custom ordering with current session list
    sessionOrder.sync(allMuxSessions.map((s) => s.name));
    if (currentSession) {
      sessionOrder.show(currentSession);
    }

    // Apply custom ordering
    const orderedNames = sessionOrder.apply(allMuxSessions.map((s) => s.name));
    const sessionByName = new Map(allMuxSessions.map((s) => [s.name, s]));
    const orderedMuxSessions = orderedNames.map((n) => sessionByName.get(n)!);

    // Batch pane counts per provider (uses BatchCapable type guard)
    const paneCountMaps = new Map<MuxProvider, Map<string, number>>();
    for (const p of allProviders) {
      if (isBatchCapable(p)) {
        paneCountMaps.set(p, p.getAllPaneCounts());
      }
    }

    const sessions: SessionData[] = orderedMuxSessions.map(({ name, createdAt, windows, dir, provider }) => {
      sessionProviders.set(name, provider);
      const git = getGitInfo(dir);
      const providerPaneCounts = paneCountMaps.get(provider);
      const panes = providerPaneCounts?.get(name) ?? provider.getPaneCount(name);

      let uptime = "";
      const diff = Math.floor(Date.now() / 1000) - createdAt;
      if (!isNaN(diff) && diff >= 0) {
        const days = Math.floor(diff / 86400);
        const hours = Math.floor((diff % 86400) / 3600);
        const mins = Math.floor((diff % 3600) / 60);
        if (days > 0) uptime = `${days}d${hours}h`;
        else if (hours > 0) uptime = `${hours}h${mins}m`;
        else uptime = `${mins}m`;
      }

      return {
        name,
        createdAt,
        dir,
        branch: git.branch,
        dirty: git.dirty,
        isWorktree: git.isWorktree,
        unseen: tracker.isUnseen(name),
        panes,
        windows,
        uptime,
        agentState: tracker.getState(name),
        agents: tracker.getAgents(name),
        eventTimestamps: tracker.getEventTimestamps(name),
        metadata: metadataStore.get(name),
      };
    });

    metadataStore.pruneSessions(new Set(sessions.map((s) => s.name)));

    if (sessions.length === 0) {
      focusedSession = null;
    } else if (!focusedSession || !sessions.some((s) => s.name === focusedSession)) {
      focusedSession = sessions.find((s) => s.name === currentSession)?.name ?? sessions[0]!.name;
    }

    // Ship effectiveThemeConfig() (PartialTheme | string) instead of just the
    // name: clients call resolveTheme() on this, and resolveTheme() merges a
    // PartialTheme palette over the default builtin. Shipping only the name
    // would force a name-based BUILTIN_THEMES lookup that fails for any
    // external theme written by the-themer (e.g. "tekapo-sunset-light"),
    // falling through to catppuccin-mocha and leaving the panel dark.
    return { type: "state", sessions, focusedSession, currentSession, theme: effectiveThemeConfig(), sidebarWidth: configuredWidth, ts: Date.now() };
  }

  let broadcastPending = false;

  function broadcastState() {
    if (broadcastPending) return;
    broadcastPending = true;
    queueMicrotask(() => {
      broadcastPending = false;
      broadcastStateImmediate();
    });
  }

  function broadcastStateImmediate() {
    invalidateCurrentSessionCache();
    tracker.pruneStuck(STUCK_RUNNING_TIMEOUT_MS);
    tracker.pruneTerminal();
    lastState = computeState();
    syncGitWatchers(lastState.sessions, broadcastState);
    syncTmuxHeaderOptions(
      {
        sessions: lastState.sessions,
        theme: resolveTheme(effectiveThemeConfig()),
        themeName: effectiveThemeName(),
        enabled: headerEnabled,
      },
      { shellTmux: tmuxHeaderShell, log: tmuxHeaderLog },
    );
    const msg = JSON.stringify(lastState);
    server.publish("sidebar", msg);
  }

  // Lightweight current-session cache — avoids a tmux subprocess per focus update
  let cachedCurrentSession: string | null = null;
  let cachedCurrentSessionTs = 0;
  const CURRENT_SESSION_CACHE_TTL = 500; // ms — short TTL, just enough to coalesce rapid switches

  function getCachedCurrentSession(): string | null {
    const now = Date.now();
    if (now - cachedCurrentSessionTs < CURRENT_SESSION_CACHE_TTL) return cachedCurrentSession;
    cachedCurrentSession = getCurrentSession();
    cachedCurrentSessionTs = now;
    return cachedCurrentSession;
  }

  function invalidateCurrentSessionCache(): void {
    cachedCurrentSessionTs = 0;
  }

  function broadcastFocusOnly(sender?: any) {
    if (!lastState) return;
    const currentSession = getCachedCurrentSession();
    lastState = { ...lastState, focusedSession, currentSession };
    const msg: FocusUpdate = { type: "focus", focusedSession, currentSession };
    const payload = JSON.stringify(msg);
    if (sender) {
      sender.publish("sidebar", payload);
    } else {
      server.publish("sidebar", payload);
    }
  }

  function moveFocus(delta: -1 | 1, sender?: any) {
    if (!lastState || lastState.sessions.length === 0) return;
    const sessions = lastState.sessions;
    const currentIdx = sessions.findIndex((s) => s.name === focusedSession);
    const newIdx = Math.max(0, Math.min(sessions.length - 1, (currentIdx === -1 ? 0 : currentIdx) + delta));
    focusedSession = sessions[newIdx]!.name;
    broadcastFocusOnly(sender);
  }

  function setFocus(name: string, sender?: any) {
    if (lastState && lastState.sessions.some((s) => s.name === name)) {
      focusedSession = name;
      broadcastFocusOnly(sender);
    }
  }

  function handleFocus(name: string): void {
    focusedSession = name;
    invalidateCurrentSessionCache();
    // Rescan pane agents when session focus changes
    refreshPaneAgents();
    const hadUnseen = tracker.handleFocus(name);
    if (hadUnseen && lastState) {
      // Patch unseen flags in-place — avoids a full computeState with many subprocesses
      const currentSession = getCachedCurrentSession();
      const updatedSessions = lastState.sessions.map((s) => {
        if (s.name !== name) return s;
        return {
          ...s,
          unseen: false,
          agents: s.agents.map((a) => ({ ...a, unseen: false })),
        };
      });
      lastState = { ...lastState, sessions: updatedSessions, focusedSession, currentSession };
      server.publish("sidebar", JSON.stringify(lastState));
    } else if (hadUnseen) {
      broadcastState();
    } else {
      broadcastFocusOnly();
    }
  }

  function switchToVisibleIndex(index: number, clientTty?: string): void {
    if (!lastState) {
      broadcastState();
    }

    if (!lastState) return;

    const idx = index - 1;
    if (idx < 0 || idx >= lastState.sessions.length) return;

    const name = lastState.sessions[idx]!.name;
    const p = sessionProviders.get(name) ?? mux;
    p.switchSession(name, clientTty);
  }

  // --- Sidebar management ---

  function getProvidersWithSidebar() {
    return allProviders.filter(isFullSidebarCapable);
  }

  /** Parse "clientTty|session|windowId" or legacy "session:windowId" context from POST body */
  function parseContext(body: string): { clientTty?: string; session: string; windowId: string } | null {
    const trimmed = body.trim().replace(/^"+|"+$/g, "").replace(/^'+|'+$/g, "");

    // New format: pipe-separated "clientTty|session|windowId"
    const pipeParts = trimmed.split("|");
    if (pipeParts.length === 3 && pipeParts[1] && pipeParts[2]) {
      const ctx = { clientTty: pipeParts[0] || undefined, session: pipeParts[1], windowId: pipeParts[2] };
      if (ctx.clientTty && ctx.session) {
        clientTtyBySession.set(ctx.session, ctx.clientTty);
      }
      return ctx;
    }

    // Legacy format: "session:windowId"
    const colonIdx = trimmed.indexOf(":");
    if (colonIdx < 1) return null;
    const session = trimmed.slice(0, colonIdx);
    const windowId = trimmed.slice(colonIdx + 1);
    if (!session || !windowId) return null;
    return { session, windowId };
  }

  // Short-lived cache for sidebar pane listings — avoid repeated tmux list-panes -a
  let sidebarPaneCache: ReturnType<typeof listSidebarPanesByProviderUncached> | null = null;
  let sidebarPaneCacheTs = 0;
  const SIDEBAR_PANE_CACHE_TTL = 300; // ms

  function listSidebarPanesByProviderUncached() {
    return getProvidersWithSidebar().map((provider) => ({
      provider,
      panes: provider.listSidebarPanes(),
    }));
  }

  function listSidebarPanesByProvider() {
    const now = Date.now();
    if (sidebarPaneCache && now - sidebarPaneCacheTs < SIDEBAR_PANE_CACHE_TTL) return sidebarPaneCache;
    sidebarPaneCache = listSidebarPanesByProviderUncached();
    sidebarPaneCacheTs = now;
    return sidebarPaneCache;
  }

  function invalidateSidebarPaneCache(): void {
    sidebarPaneCache = null;
    sidebarPaneCacheTs = 0;
  }

  const pendingSidebarSpawns = new Set<string>();

  function toggleSidebar(ctx?: { session: string; windowId: string }): void {
    const providers = getProvidersWithSidebar();
    if (providers.length === 0) {
      log("toggle", "SKIP — no providers with sidebar methods");
      return;
    }

    invalidateSidebarPaneCache();
    if (sidebarVisible) {
      for (const p of providers) {
        const panes = p.listSidebarPanes();
        log("toggle", "OFF — hiding panes", { provider: p.name, count: panes.length });
        for (const pane of panes) {
          p.hideSidebar(pane.paneId);
        }
      }
      sidebarVisible = false;
    } else {
      sidebarVisible = true;
      setPendingEnforcement();
      for (const p of providers) {
        const allWindows = p.listActiveWindows();
        log("toggle", "ON — spawning in active windows", { provider: p.name, count: allWindows.length });
        for (const w of allWindows) {
          ensureSidebarInWindow(p, { session: w.sessionName, windowId: w.id });
        }
      }
      enforceSidebarWidth();
      server.publish("sidebar", JSON.stringify({ type: "re-identify" }));
    }
    log("toggle", "done", { sidebarVisible });
  }

  function ensureSidebarInWindow(provider?: ReturnType<typeof getProvidersWithSidebar>[number], ctx?: { session: string; windowId: string }): void {
    // If no specific provider, try to find one for the session
    const p = provider ?? (() => {
      const providers = getProvidersWithSidebar();
      if (ctx?.session) {
        const sessionProvider = sessionProviders.get(ctx.session);
        return providers.find((pp) => pp === sessionProvider) ?? providers[0];
      }
      return providers[0];
    })();
    if (!p || !sidebarVisible) {
      log("ensure", "SKIP", { hasProvider: !!p, sidebarVisible });
      return;
    }

    const curSession = ctx?.session ?? getCurrentSession();
    if (!curSession) {
      log("ensure", "SKIP — no current session");
      return;
    }

    const windowId = ctx?.windowId ?? p.getCurrentWindowId();
    if (!windowId) {
      log("ensure", "SKIP — could not get window_id");
      return;
    }

    const spawnKey = `${p.name}:${windowId}`;
    if (pendingSidebarSpawns.has(spawnKey)) {
      log("ensure", "SKIP — spawn already in progress", { curSession, windowId, provider: p.name });
      return;
    }

    // Use cached pane listing to avoid redundant tmux list-panes -a calls
    const allPanesByProvider = listSidebarPanesByProvider();
    const providerEntry = allPanesByProvider.find((e) => e.provider === p);
    const existingPanes = providerEntry?.panes ?? [];
    const hasInWindow = existingPanes.some((ep) => ep.windowId === windowId);
    log("ensure", "checking window", {
      curSession, windowId, existingPanes: existingPanes.length,
      hasInWindow, paneIds: existingPanes.map((x) => `${x.paneId}@${x.windowId}`),
    });

    if (!hasInWindow) {
      invalidateSidebarPaneCache();
      pendingSidebarSpawns.add(spawnKey);
      log("ensure", "SPAWNING sidebar", { curSession, windowId, sidebarWidth: configuredWidth, sidebarPosition, scriptsDir });
      try {
        const newPaneId = p.spawnSidebar(curSession, windowId, configuredWidth, sidebarPosition, scriptsDir);
        log("ensure", "spawn result", { newPaneId });
        // Do NOT refocus the main pane here — the TUI handles it.
        // For fresh spawns, the TUI refocuses after capability detection.
        // For stash restores, the TUI refocuses after restoreTerminalModes
        // responses settle. Refocusing immediately from the server causes
        // capability query responses to leak as garbage escape sequences.
      } finally {
        pendingSidebarSpawns.delete(spawnKey);
      }
    }
    // Always enforce width — session switches can change window width,
    // causing tmux to proportionally redistribute pane sizes.
    enforceSidebarWidth();
  }

  // Debounced ensure-sidebar — collapses rapid hook-fired calls during fast
  // session switching into a single check after switching settles.
  let ensureSidebarTimer: ReturnType<typeof setTimeout> | null = null;
  let ensureSidebarPendingCtx: { session: string; windowId: string } | undefined;

  function debouncedEnsureSidebar(ctx?: { session: string; windowId: string }): void {
    if (ctx) ensureSidebarPendingCtx = ctx;
    if (ensureSidebarTimer) clearTimeout(ensureSidebarTimer);
    ensureSidebarTimer = setTimeout(() => {
      ensureSidebarTimer = null;
      const nextCtx = ensureSidebarPendingCtx;
      ensureSidebarPendingCtx = undefined;
      ensureSidebarInWindow(undefined, nextCtx);
    }, 150);
  }

  function quitAll(): void {
    log("quit", "killing all sidebar panes");
    for (const p of getProvidersWithSidebar()) {
      const panes = p.listSidebarPanes();
      log("quit", "found panes to kill", { provider: p.name, count: panes.length });
      for (const pane of panes) {
        p.killSidebarPane(pane.paneId);
      }
    }
    // Provider-specific cleanup (uses type guard)
    for (const p of getProvidersWithSidebar()) {
      p.cleanupSidebar();
    }
    server.publish("sidebar", JSON.stringify({ type: "quit" }));
    sidebarVisible = false;
    cleanup();
    process.exit(0);
  }

  // --- Sidebar width enforcement ---

  // When true, the next report-width from a TUI is a proportional resize echo
  // (caused by session switch, terminal resize, etc.), NOT a user drag.
  // Set by /focus, /ensure-sidebar, /client-resized hooks; cleared by report-width
  // or auto-expires after 500ms (in case no SIGWINCH fires, e.g. width didn't change).
  let pendingEnforcement = false;
  let pendingEnforcementTimer: ReturnType<typeof setTimeout> | null = null;
  let saveTimer: ReturnType<typeof setTimeout> | null = null;

  function cancelPendingSave() {
    if (saveTimer) { clearTimeout(saveTimer); saveTimer = null; }
  }

  function setPendingEnforcement() {
    cancelPendingSave();
    pendingEnforcement = true;
    if (pendingEnforcementTimer) clearTimeout(pendingEnforcementTimer);
    pendingEnforcementTimer = setTimeout(() => {
      if (pendingEnforcement) {
        pendingEnforcement = false;
      }
      pendingEnforcementTimer = null;
    }, 500);
  }

  function enforceSidebarWidth(skipSession?: string) {
    invalidateSidebarPaneCache();
    for (const { provider, panes } of listSidebarPanesByProvider()) {
      for (const pane of panes) {
        if (pane.width === configuredWidth) {
          continue;
        }
        if (skipSession && pane.sessionName === skipSession) {
          continue;
        }
        log("enforce", `${pane.paneId} ${pane.width}→${configuredWidth}`);
        provider.resizeSidebarPane(pane.paneId, configuredWidth);
      }
    }
  }

  // --- Focus agent pane (click-to-focus from TUI) ---

  /** Walk up to 3 levels of child processes looking for a command matching any pattern */
  function matchProcessTree(pid: string, patterns: string[], depth = 0): boolean {
    if (depth > 2) return false;
    const children = shell(["pgrep", "-P", pid]);
    if (!children) return false;
    for (const childPid of children.split("\n")) {
      const trimmed = childPid.trim();
      if (!trimmed) continue;
      const childCmd = shell(["ps", "-p", trimmed, "-o", "comm="]);
      if (childCmd && patterns.some((pat) => commMatches(childCmd.toLowerCase(), pat))) return true;
      if (matchProcessTree(trimmed, patterns, depth + 1)) return true;
    }
    return false;
  }

  const AGENT_TITLE_PATTERNS: Record<string, string[]> = {
    amp: ["amp"],
    "claude-code": ["claude"],
    codex: ["codex"],
    opencode: ["opencode"],
    pi: ["pi"],
  };

  const PANE_HIGHLIGHT_BORDER = "fg=#fab387,bold";
  const PANE_HIGHLIGHT_MS = 300;
  const pendingHighlightResets = new Map<string, ReturnType<typeof setTimeout>>();

  /** Walk child processes (up to 3 levels) to find a process matching `name`, returning its PID. */
  function findChildPid(pid: string, name: string, depth = 0): string | undefined {
    if (depth > 2) return undefined;
    const children = shell(["pgrep", "-P", pid]);
    if (!children) return undefined;
    for (const childPid of children.split("\n")) {
      const trimmed = childPid.trim();
      if (!trimmed) continue;
      const childCmd = shell(["ps", "-p", trimmed, "-o", "comm="]);
      if (childCmd?.trim().toLowerCase().includes(name)) return trimmed;
      const found = findChildPid(trimmed, name, depth + 1);
      if (found) return found;
    }
    return undefined;
  }

  type PaneEntry = { id: string; pid: string; cmd: string; title: string };

  /** Claude Code: ~/.claude/sessions/<pid>.json → sessionId */
  function resolveClaudeCodePane(panes: PaneEntry[], threadId: string): string | undefined {
    const sessionsDir = join(homedir(), ".claude", "sessions");
    for (const pane of panes) {
      const agentPid = findChildPid(pane.pid, "claude");
      if (!agentPid) continue;
      try {
        const data = JSON.parse(readFileSync(join(sessionsDir, `${agentPid}.json`), "utf-8"));
        if (data.sessionId === threadId) return pane.id;
      } catch {}
    }
    return undefined;
  }

  /** Codex: logs_1.sqlite process_uuid='pid:<PID>:*' → thread_id */
  function resolveCodexPane(panes: PaneEntry[], threadId: string): string | undefined {
    const dbPath = join(process.env.CODEX_HOME ?? join(homedir(), ".codex"), "logs_1.sqlite");
    let db: any;
    try {
      const { Database } = require("bun:sqlite");
      db = new Database(dbPath, { readonly: true });
    } catch { return undefined; }

    try {
      for (const pane of panes) {
        const agentPid = findChildPid(pane.pid, "codex");
        if (!agentPid) continue;
        const row = db.query(
          `SELECT thread_id FROM logs WHERE process_uuid LIKE ? AND thread_id IS NOT NULL ORDER BY ts DESC LIMIT 1`,
        ).get(`pid:${agentPid}:%`);
        if (row?.thread_id === threadId) return pane.id;
      }
    } finally { try { db.close(); } catch {} }
    return undefined;
  }

  /** OpenCode: lsof → log file → grep session ID */
  function resolveOpenCodePane(panes: PaneEntry[], threadId: string): string | undefined {
    for (const pane of panes) {
      const agentPid = findChildPid(pane.pid, "opencode");
      if (!agentPid) continue;
      const lsofOut = shell(["lsof", "-p", agentPid]);
      if (!lsofOut) continue;
      // Find the log file path from open file descriptors
      const logLine = lsofOut.split("\n").find((l) => l.includes("/opencode/log/") && l.endsWith(".log"));
      if (!logLine) continue;
      // Extract absolute path — lsof NAME column starts at the last recognized path
      const pathMatch = logLine.match(/\s(\/\S+\.log)$/);
      if (!pathMatch) continue;
      try {
        const logText = readFileSync(pathMatch[1], "utf-8");
        const match = logText.match(/ses_[A-Za-z0-9]+/);
        if (match?.[0] === threadId) return pane.id;
      } catch {}
    }
    return undefined;
  }

  /** Resolve a tmux pane ID for an agent using all available resolution strategies. */
  function resolveAgentPaneId(sessionName: string, agentName: string, threadId?: string, threadName?: string): string | undefined {
    const p = sessionProviders.get(sessionName) ?? mux;
    if (p.name !== "tmux") return undefined;

    const patterns = AGENT_TITLE_PATTERNS[agentName];
    if (!patterns) return undefined;

    const raw = shell([
      "tmux", "list-panes", "-s", "-t", sessionName,
      "-F", "#{pane_id}|#{pane_pid}|#{pane_current_command}|#{pane_title}",
    ]);
    if (!raw) return undefined;

    const panes = raw.split("\n")
      .map((line) => {
        const idx1 = line.indexOf("|");
        const idx2 = line.indexOf("|", idx1 + 1);
        const idx3 = line.indexOf("|", idx2 + 1);
        return {
          id: line.slice(0, idx1),
          pid: line.slice(idx1 + 1, idx2),
          cmd: line.slice(idx2 + 1, idx3),
          title: line.slice(idx3 + 1),
        };
      });

    const sidebarPaneIds = new Set<string>();
    for (const { panes: sbPanes } of listSidebarPanesByProvider()) {
      for (const sb of sbPanes) sidebarPaneIds.add(sb.paneId);
    }
    const nonSidebar = panes.filter((p) => !sidebarPaneIds.has(p.id));

    let targetPaneId: string | undefined;

    if (agentName === "claude-code" && threadId) {
      targetPaneId = resolveClaudeCodePane(nonSidebar, threadId);
    }
    if (!targetPaneId && agentName === "amp" && threadName) {
      targetPaneId = nonSidebar
        .find((p) => p.title.toLowerCase().startsWith("amp - ") && p.title.includes(threadName))
        ?.id;
    }
    if (!targetPaneId && agentName === "codex" && threadId) {
      targetPaneId = resolveCodexPane(nonSidebar, threadId);
    }
    if (!targetPaneId && agentName === "opencode" && threadId) {
      targetPaneId = resolveOpenCodePane(nonSidebar, threadId);
    }
    if (!targetPaneId) {
      targetPaneId = nonSidebar
        .find((p) => patterns.some((pat) => p.title.toLowerCase().includes(pat)))
        ?.id;
    }
    if (!targetPaneId) {
      for (const pane of nonSidebar) {
        if (matchProcessTree(pane.pid, patterns)) {
          targetPaneId = pane.id;
          break;
        }
      }
    }
    return targetPaneId;
  }

  function focusAgentPane(sessionName: string, agentName: string, threadId?: string, threadName?: string): void {
    log("focus-agent-pane", "received", { sessionName, agentName, threadId, threadName });
    const targetPaneId = resolveAgentPaneId(sessionName, agentName, threadId, threadName);
    if (!targetPaneId) return;

    log("focus-agent-pane", "focusing", { sessionName, agentName, paneId: targetPaneId });

    // Switch to the window containing the target pane first,
    // otherwise select-pane alone won't work across windows
    const windowId = shell(["tmux", "display-message", "-t", targetPaneId, "-p", "#{window_id}"]);
    if (windowId) {
      shell(["tmux", "select-window", "-t", windowId.trim()]);
    }
    shell(["tmux", "select-pane", "-t", targetPaneId]);

    const existing = pendingHighlightResets.get(targetPaneId);
    if (existing) clearTimeout(existing);

    shell(["tmux", "set-option", "-p", "-t", targetPaneId, "pane-active-border-style", PANE_HIGHLIGHT_BORDER]);
    shell(["tmux", "select-pane", "-t", targetPaneId, "-P", "bg=#2a2a4a"]);
    pendingHighlightResets.set(
      targetPaneId,
      setTimeout(() => {
        shell(["tmux", "set-option", "-p", "-t", targetPaneId, "-u", "pane-active-border-style"]);
        shell(["tmux", "select-pane", "-t", targetPaneId, "-P", ""]);
        pendingHighlightResets.delete(targetPaneId);
      }, PANE_HIGHLIGHT_MS),
    );
  }

  function killAgentPane(sessionName: string, agentName: string, threadId?: string, threadName?: string): void {
    log("kill-agent-pane", "received", { sessionName, agentName, threadId, threadName });
    const targetPaneId = resolveAgentPaneId(sessionName, agentName, threadId, threadName);
    if (!targetPaneId) return;

    log("kill-agent-pane", "killing", { sessionName, agentName, paneId: targetPaneId });
    shell(["tmux", "kill-pane", "-t", targetPaneId]);
  }

  // --- Pane agent scanning (detect agents running in current session panes) ---

  // Pane presence is now folded into the tracker via applyPanePresence().

  /** Build parent→children map from a single ps snapshot (avoids per-pane pgrep calls). */
  function buildProcessTree(): { childrenOf: Map<number, number[]>; commOf: Map<number, string> } {
    const childrenOf = new Map<number, number[]>();
    const commOf = new Map<number, string>();
    const psResult = Bun.spawnSync(["ps", "-eo", "pid=,ppid=,comm="], { stdout: "pipe", stderr: "pipe" });
    for (const line of psResult.stdout.toString().trim().split("\n")) {
      const parts = line.trim().split(/\s+/);
      if (parts.length < 3) continue;
      const pid = parseInt(parts[0], 10);
      const ppid = parseInt(parts[1], 10);
      const comm = parts.slice(2).join(" ").toLowerCase();
      if (isNaN(pid) || isNaN(ppid)) continue;
      commOf.set(pid, comm);
      let arr = childrenOf.get(ppid);
      if (!arr) { arr = []; childrenOf.set(ppid, arr); }
      arr.push(pid);
    }
    return { childrenOf, commOf };
  }

  // commMatches is hoisted to module scope (see export at file bottom) so
  // unit tests can exercise the boundary rules without spinning up a server.

  /** Walk up to 3 levels of child processes using a pre-built process tree. */
  function matchProcessTreeFast(
    pid: number, patterns: string[],
    tree: ReturnType<typeof buildProcessTree>, depth = 0,
  ): boolean {
    if (depth > 2) return false;
    const children = tree.childrenOf.get(pid);
    if (!children) return false;
    for (const childPid of children) {
      const comm = tree.commOf.get(childPid);
      if (comm && patterns.some((pat) => commMatches(comm, pat))) return true;
      if (matchProcessTreeFast(childPid, patterns, tree, depth + 1)) return true;
    }
    return false;
  }

  /** Scan all panes across all tmux sessions and identify running agents.
   *  Returns only {agent, paneId} — no threadId, status, or threadName.
   *  Watchers are the single source of truth for those fields. */
  function scanAllTmuxPaneAgents(): Map<string, import("../contracts/agent").PanePresenceInput[]> {
    const result = new Map<string, import("../contracts/agent").PanePresenceInput[]>();

    const raw = shell([
      "tmux", "list-panes", "-a",
      "-F", "#{session_name}|#{pane_id}|#{pane_pid}|#{pane_current_command}|#{pane_title}",
    ]);
    if (!raw) return result;

    const panes = raw.split("\n").filter(Boolean).map((line) => {
      const idx1 = line.indexOf("|");
      const idx2 = line.indexOf("|", idx1 + 1);
      const idx3 = line.indexOf("|", idx2 + 1);
      const idx4 = line.indexOf("|", idx3 + 1);
      return {
        session: line.slice(0, idx1),
        id: line.slice(idx1 + 1, idx2),
        pid: parseInt(line.slice(idx2 + 1, idx3), 10),
        cmd: line.slice(idx3 + 1, idx4),
        title: line.slice(idx4 + 1),
      };
    });

    // Exclude sidebar panes
    const sidebarPaneIds = new Set<string>();
    for (const { panes: sbPanes } of listSidebarPanesByProvider()) {
      for (const sb of sbPanes) sidebarPaneIds.add(sb.paneId);
    }

    const nonSidebar = panes.filter((p) => !sidebarPaneIds.has(p.id));
    if (nonSidebar.length === 0) return result;

    // Build process tree once for all panes
    const tree = buildProcessTree();

    for (const pane of nonSidebar) {
      for (const [agentName, patterns] of Object.entries(AGENT_TITLE_PATTERNS)) {
        // Only use process tree matching — title matching produces false positives
        // (e.g. an Amp thread named "Detect Claude session names" matches "claude")
        if (!matchProcessTreeFast(pane.pid, patterns, tree)) continue;

        let sessionAgents = result.get(pane.session);
        if (!sessionAgents) {
          sessionAgents = [];
          result.set(pane.session, sessionAgents);
        }
        sessionAgents.push({ agent: agentName, paneId: pane.id });
        break; // One agent per pane — first match wins (ordered so parents precede child tools)
      }
    }

    return result;
  }

  /** Refresh pane agent presence by scanning tmux panes and folding results into the tracker. */
  function refreshPaneAgents(): void {
    const hasTmux = allProviders.some((p) => p.name === "tmux");
    if (!hasTmux) {
      // No tmux provider — mark all previously-alive agents as exited
      // by applying empty presence for each tracked session
      // (applyPanePresence handles the exited transition internally)
      return;
    }

    const nextBySession = scanAllTmuxPaneAgents();
    let changed = false;

    // Apply presence for sessions that have pane agents
    for (const [session, paneAgents] of nextBySession) {
      if (tracker.applyPanePresence(session, paneAgents)) changed = true;
    }

    // For sessions NOT in the scan, apply empty presence to transition alive → exited
    if (lastState) {
      for (const s of lastState.sessions) {
        if (!nextBySession.has(s.name)) {
          if (tracker.applyPanePresence(s.name, [])) changed = true;
        }
      }
    }

    if (changed) broadcastState();
  }

  // --- Pane agent polling (detect agents in current session every 3s) ---

  const PANE_SCAN_INTERVAL_MS = 3_000;
  let paneScanTimer: ReturnType<typeof setInterval> | null = null;

  function startPaneScan() {
    paneScanTimer = setInterval(() => {
      if (clientCount === 0) return;
      refreshPaneAgents();
    }, PANE_SCAN_INTERVAL_MS);
  }

  function handleCommand(cmd: ClientCommand, ws: any) {
    switch (cmd.type) {
      case "identify":
        clientTtys.set(ws, cmd.clientTty);
        break;
      case "switch-session": {
        // Resolve TTY: hook-derived (authoritative) > client-provided > stored
        const clientSess = clientSessionNames.get(ws);
        const tty = (clientSess ? clientTtyBySession.get(clientSess) : undefined)
          ?? cmd.clientTty ?? clientTtys.get(ws);
        log("switch-session", "switching", { target: cmd.name, tty, clientSess });
        const p = sessionProviders.get(cmd.name) ?? mux;
        p.switchSession(cmd.name, tty);

        // Optimistic server-side focus update — so other TUI instances see the
        // change immediately via broadcastFocusOnly, without waiting for the
        // tmux hook round-trip. The hook's /focus POST will reconcile if needed.
        focusedSession = cmd.name;
        cachedCurrentSession = cmd.name;
        cachedCurrentSessionTs = Date.now();
        const hadUnseen = tracker.handleFocus(cmd.name);
        if (hadUnseen) {
          broadcastState();
        } else {
          broadcastFocusOnly();
        }
        break;
      }
      case "switch-index": {
        const clientSess = clientSessionNames.get(ws);
        const tty = (clientSess ? clientTtyBySession.get(clientSess) : undefined)
          ?? clientTtys.get(ws);
        switchToVisibleIndex(cmd.index, tty);
        break;
      }
      case "new-session":
        mux.createSession();
        broadcastState();
        break;
      case "hide-session":
        sessionOrder.hide(cmd.name);
        broadcastState();
        break;
      case "show-all-sessions":
        sessionOrder.showAll();
        broadcastState();
        break;
      case "kill-session": {
        const p = sessionProviders.get(cmd.name) ?? mux;
        // If killing the current session, switch to the adjacent session in sidebar order
        const currentBefore = getCurrentSession();
        if (currentBefore === cmd.name) {
          const allNames = p.listSessions().map((s) => s.name);
          const visible = sessionOrder.apply(allNames);
          const idx = visible.indexOf(cmd.name);
          // Prefer the session before, then after, in sidebar order
          const fallback = visible[idx - 1] ?? visible[idx + 1];
          if (fallback) {
            const tty = clientTtyBySession.get(cmd.name);
            p.switchSession(fallback, tty);
          }
        }
        p.killSession(cmd.name);
        broadcastState();
        break;
      }
      case "reorder-session":
        sessionOrder.reorder(cmd.name, cmd.delta);
        broadcastState();
        break;
      case "refresh":
        broadcastState();
        break;
      case "move-focus":
        moveFocus(cmd.delta, ws);
        break;
      case "focus-session":
        setFocus(cmd.name, ws);
        break;
      case "mark-seen":
        if (tracker.markSeen(cmd.name)) broadcastState();
        break;
      case "dismiss-agent":
        if (tracker.dismiss(cmd.session, cmd.agent, cmd.threadId)) broadcastState();
        break;
      case "set-theme":
        currentTheme = cmd.theme;
        saveConfig({ theme: cmd.theme });
        broadcastState();
        break;
      case "quit":
        quitAll();
        break;
      case "identify-pane":
        // Store this client's session, reply with session + authoritative client TTY
        clientSessionNames.set(ws, cmd.sessionName);
        ws.send(JSON.stringify({
          type: "your-session",
          name: cmd.sessionName,
          clientTty: clientTtyBySession.get(cmd.sessionName) ?? null,
        }));
        break;
      case "focus-agent-pane":
        log("handleCommand", "focus-agent-pane received", { session: cmd.session, agent: cmd.agent, threadId: cmd.threadId, threadName: cmd.threadName });
        focusAgentPane(cmd.session, cmd.agent, cmd.threadId, cmd.threadName);
        break;
      case "kill-agent-pane":
        log("handleCommand", "kill-agent-pane received", { session: cmd.session, agent: cmd.agent, threadId: cmd.threadId, threadName: cmd.threadName });
        killAgentPane(cmd.session, cmd.agent, cmd.threadId, cmd.threadName);
        break;
      case "report-width": {
        if (!sidebarVisible) {
          break;
        }
        // Get window width from the reporting client's sidebar pane for the max clamp
        const session = clientSessionNames.get(ws) ?? null;
        let windowWidth: number | undefined;
        if (session) {
          for (const { panes } of listSidebarPanesByProvider()) {
            const pane = panes.find((p) => p.sessionName === session);
            if (pane?.windowWidth) { windowWidth = pane.windowWidth; break; }
          }
        }
        const reported = clampSidebarWidth(cmd.width, windowWidth);
        if (pendingEnforcement) {
          pendingEnforcement = false;
          enforceSidebarWidth();
          break;
        }
        if (reported === configuredWidth) {
          break;
        }
        // Debounce the save — if enforcement fires within the window, it was a reflow
        cancelPendingSave();
        saveTimer = setTimeout(() => {
          saveTimer = null;
          configuredWidth = reported;
          saveConfig({ sidebarWidth: configuredWidth });
          broadcastState();
          enforceSidebarWidth(session ?? undefined);
        }, SAVE_DEBOUNCE_MS);
        break;
      }
      case "equalize-width": {
        cancelPendingSave();
        const sessions = lastState?.sessions ?? [];
        configuredWidth = sessions.length > 0
          ? computeMinSidebarWidth(sessions)
          : ABSOLUTE_MIN_SIDEBAR_WIDTH;
        saveConfig({ sidebarWidth: configuredWidth });
        enforceSidebarWidth();
        broadcastState();
        break;
      }
    }
  }
  function cleanup() {
    for (const w of allWatchers) w.stop();
    if (watcherBroadcastTimer) clearTimeout(watcherBroadcastTimer);
    if (debounceTimer) clearTimeout(debounceTimer);
    if (paneScanTimer) clearInterval(paneScanTimer);
    for (const timer of pendingHighlightResets.values()) clearTimeout(timer);
    pendingHighlightResets.clear();
    for (const watcher of gitHeadWatchers.values()) watcher.close();
    gitHeadWatchers.clear();
    if (idleTimer) clearTimeout(idleTimer);
    if (externalThemeWatcher) {
      try { externalThemeWatcher.close(); } catch {}
      externalThemeWatcher = null;
    }
    try { unlinkSync(PID_FILE); } catch {}
    for (const p of allProviders) p.cleanupHooks();
  }

  // --- Write PID + start server ---

  writeFileSync(PID_FILE, String(process.pid));

  const server = Bun.serve({
    port: SERVER_PORT,
    hostname: SERVER_HOST,
    async fetch(req, server) {
      const url = new URL(req.url);

      if (req.method === "GET" && url.pathname === "/state") {
        const state = computeState();
        return new Response(JSON.stringify(state, null, 2), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }

      if (req.method === "POST" && url.pathname === "/refresh") {
        broadcastState();
        return new Response("ok", { status: 200 });
      }

      // Hook endpoint: receives lifecycle events from agent processes.
      // Always returns 200 — hook failures must never block the agent.
      if (req.method === "POST" && url.pathname === "/hook") {
        try {
          const body = (await req.json()) as unknown;
          if (body && typeof body === "object") {
            const payload = body as import("../contracts/agent-watcher").HookPayload;
            for (const w of allWatchers) {
              if (isHookReceiver(w)) w.handleHook(payload);
            }
          }
        } catch {}
        return new Response("ok", { status: 200 });
      }

      if (req.method === "POST" && url.pathname === "/focus") {
        try {
          const body = await req.text();
          const ctx = parseContext(body);
          if (ctx) {
            setPendingEnforcement();
            handleFocus(ctx.session);
          } else {
            // Legacy: body is just the session name
            const name = body.trim().replace(/^"+|"+$/g, "");
            if (name) {
              setPendingEnforcement();
              handleFocus(name);
            }
          }
        } catch {}
        return new Response("ok", { status: 200 });
      }

      if (req.method === "POST" && url.pathname === "/pane-focus") {
        try {
          const paneId = (await req.text()).trim().replace(/^"+|"+$/g, "").replace(/^'+|'+$/g, "");
          if (paneId) {
            const msg: import("../shared").PaneFocusUpdate = { type: "pane-focus", paneId };
            server.publish("sidebar", JSON.stringify(msg));
          }
        } catch {}
        return new Response("ok", { status: 200 });
      }

      if (req.method === "POST" && url.pathname === "/toggle") {
        try {
          const body = await req.text();
          const ctx = parseContext(body) ?? undefined;
          log("http", "POST /toggle", { ctx });
          toggleSidebar(ctx);
          broadcastState();
        } catch {}
        return new Response("ok", { status: 200 });
      }

      if (req.method === "POST" && url.pathname === "/quit") {
        log("http", "POST /quit");
        quitAll();
        return new Response("ok", { status: 200 });
      }

      if (req.method === "POST" && url.pathname === "/restart") {
        const skipReload = url.searchParams.get("reload-tui") === "false";
        log("http", "POST /restart", { reloadTui: !skipReload });
        // Respond before shutting down so the caller gets confirmation
        setTimeout(() => {
          cleanup();
          server.stop();
          // Re-exec the server with the same entry point
          const serverEntry = join(
            process.env.TCM_DIR ?? join(import.meta.dir, "..", "..", "..", ".."),
            "apps", "server", "src", "main.ts",
          );
          const proc = Bun.spawn([process.execPath, "run", serverEntry], {
            stdio: ["ignore", "ignore", "ignore"],
            env: {
              ...process.env,
              // Tell the new server to cycle sidebars after startup
              TCM_RELOAD_TUI: skipReload ? "" : "1",
            },
          });
          proc.unref();
          process.exit(0);
        }, 50);
        return new Response("restarting", { status: 200 });
      }

      if (req.method === "POST" && url.pathname === "/switch-index") {
        try {
          const index = Number.parseInt(url.searchParams.get("index") ?? "", 10);
          if (Number.isNaN(index)) {
            return new Response("missing index", { status: 400 });
          }
          const body = await req.text();
          const ctx = parseContext(body) ?? undefined;
          log("http", "POST /switch-index", { index, ctx });
          switchToVisibleIndex(index, ctx?.clientTty);
        } catch {}
        return new Response("ok", { status: 200 });
      }

      if (req.method === "POST" && url.pathname === "/ensure-sidebar") {
        try {
          const body = await req.text();
          const ctx = parseContext(body) ?? undefined;
          setPendingEnforcement();
          log("http", "POST /ensure-sidebar", { sidebarVisible, ctx });
          debouncedEnsureSidebar(ctx ?? undefined);
        } catch {}
        return new Response("ok", { status: 200 });
      }

      // client-resized hook: terminal window changed size — enforce stored width
      if (req.method === "POST" && url.pathname === "/client-resized") {
        setPendingEnforcement();
        if (sidebarVisible) {
          enforceSidebarWidth();
        }
        return new Response("ok", { status: 200 });
      }

      // pane-exited hook: a pane closed — kill orphaned sidebar panes
      if (req.method === "POST" && url.pathname === "/pane-exited") {
        if (sidebarVisible) {
          invalidateSidebarPaneCache();
          for (const { provider } of listSidebarPanesByProvider()) {
            provider.killOrphanedSidebarPanes();
          }
        }
        return new Response("ok", { status: 200 });
      }

      if (req.method === "POST" && url.pathname === "/set-status") {
        try {
          const body = await req.json() as { session?: string; text?: string | null; tone?: string };
          if (!body.session || typeof body.session !== "string") {
            return new Response("missing session", { status: 400 });
          }
          if (body.text === null || body.text === undefined) {
            metadataStore.setStatus(body.session, null);
          } else if (typeof body.text !== "string") {
            return new Response("text must be a string or null", { status: 400 });
          } else {
            metadataStore.setStatus(body.session, { text: body.text, tone: body.tone as any });
          }
          broadcastState();
          return new Response(null, { status: 204 });
        } catch {
          return new Response("invalid json", { status: 400 });
        }
      }

      if (req.method === "POST" && url.pathname === "/set-progress") {
        try {
          const body = await req.json() as { session?: string; current?: number; total?: number; percent?: number; label?: string; clear?: boolean };
          if (!body.session || typeof body.session !== "string") {
            return new Response("missing session", { status: 400 });
          }
          if (body.clear) {
            metadataStore.setProgress(body.session, null);
          } else {
            metadataStore.setProgress(body.session, {
              current: body.current,
              total: body.total,
              percent: body.percent,
              label: body.label,
            });
          }
          broadcastState();
          return new Response(null, { status: 204 });
        } catch {
          return new Response("invalid json", { status: 400 });
        }
      }

      if (req.method === "POST" && url.pathname === "/log") {
        try {
          const body = await req.json() as { session?: string; message?: string; tone?: string; source?: string };
          if (!body.session || typeof body.session !== "string") {
            return new Response("missing session", { status: 400 });
          }
          if (!body.message || typeof body.message !== "string") {
            return new Response("missing message", { status: 400 });
          }
          metadataStore.appendLog(body.session, {
            message: body.message,
            tone: body.tone as any,
            source: body.source,
          });
          broadcastState();
          return new Response(null, { status: 204 });
        } catch {
          return new Response("invalid json", { status: 400 });
        }
      }

      if (req.method === "POST" && url.pathname === "/clear-log") {
        try {
          const body = await req.json() as { session?: string };
          if (!body.session || typeof body.session !== "string") {
            return new Response("missing session", { status: 400 });
          }
          metadataStore.clearLogs(body.session);
          broadcastState();
          return new Response(null, { status: 204 });
        } catch {
          return new Response("invalid json", { status: 400 });
        }
      }

      if (req.method === "POST" && url.pathname === "/notify") {
        try {
          const body = await req.json() as { session?: string; message?: string; tone?: string; source?: string };
          if (!body.session || typeof body.session !== "string") {
            return new Response("missing session", { status: 400 });
          }
          if (!body.message || typeof body.message !== "string") {
            return new Response("missing message", { status: 400 });
          }
          metadataStore.appendLog(body.session, {
            message: body.message,
            tone: body.tone as any,
            source: body.source,
          });
          broadcastState();
          return new Response(null, { status: 204 });
        } catch {
          return new Response("invalid json", { status: 400 });
        }
      }

      if (server.upgrade(req)) return;
      return new Response("tcm server", { status: 200 });
    },
    websocket: {
      open(ws) {
        ws.subscribe("sidebar");
        clientCount++;
        log("ws", "client connected", { clientCount });
        if (idleTimer) {
          clearTimeout(idleTimer);
          idleTimer = null;
        }
        if (lastState) {
          ws.send(JSON.stringify(lastState));
        } else {
          broadcastState();
        }
      },
      close(ws) {
        ws.unsubscribe("sidebar");
        clientCount--;
        if (clientCount < 0) clientCount = 0;
        log("ws", "client disconnected", { clientCount });
        if (clientCount === 0 && !idleTimer) {
          log("ws", "no clients remaining, starting idle timer", { timeoutMs: SERVER_IDLE_TIMEOUT_MS });
          idleTimer = setTimeout(() => {
            log("ws", "idle timeout reached, shutting down");
            quitAll();
          }, SERVER_IDLE_TIMEOUT_MS);
        }
      },
      message(ws, msg) {
        try {
          const cmd = JSON.parse(msg as string) as ClientCommand;
          log("ws", "command", { type: cmd.type });
          handleCommand(cmd, ws);
        } catch {}
      },
    },
  });

  // --- Bootstrap ---

  // Install per-provider tmux hooks. We log around the call (and verify
  // installation for the tmux provider) because setupHooks() is fire-and-
  // forget today: a silent tmux failure here is the difference between
  // "agents update live" and "sidebar feels frozen". See docs in
  // packages/mux/providers/tmux/src/provider.ts -> setupHooks().
  for (const p of allProviders) {
    log("bootstrap", "installing hooks", { provider: p.name });
    try {
      p.setupHooks(SERVER_HOST, SERVER_PORT);
    } catch (err) {
      log("bootstrap", "setupHooks threw", { provider: p.name, error: err instanceof Error ? err.message : String(err) });
      continue;
    }
    if (p.name === "tmux") verifyTmuxHooksInstalled();
  }

  // Detect pre-existing sidebar panes (e.g. after a server restart while
  // TUI sidebars are still running and reconnecting)
  {
    let existingSidebars = 0;
    for (const { panes } of listSidebarPanesByProvider()) {
      existingSidebars += panes.length;
    }
    if (existingSidebars > 0) {
      sidebarVisible = true;
      log("bootstrap", "detected existing sidebar panes", { count: existingSidebars });
      enforceSidebarWidth();

      // Reload TUI: kill all sidebar panes (and stash) then respawn fresh.
      // Triggered by /restart (default) — opt out with ?reload-tui=false.
      // Note: the old toggle cycle (hide/show) didn't work because tmux's
      // spawnSidebar restores stashed panes instead of spawning fresh processes.
      if (process.env.TCM_RELOAD_TUI === "1") {
        delete process.env.TCM_RELOAD_TUI;
        log("bootstrap", "reloading TUI — killing and respawning sidebars");
        setTimeout(() => {
          const providers = getProvidersWithSidebar();
          for (const p of providers) {
            const panes = p.listSidebarPanes();
            for (const pane of panes) {
              log("bootstrap", "killing sidebar pane for reload", { paneId: pane.paneId, session: pane.sessionName });
              p.killSidebarPane(pane.paneId);
            }
            p.cleanupSidebar();
          }
          invalidateSidebarPaneCache();
          // Respawn fresh sidebars in all active windows
          setTimeout(() => {
            for (const p of providers) {
              for (const w of p.listActiveWindows()) {
                ensureSidebarInWindow(p, { session: w.sessionName, windowId: w.id });
              }
            }
            enforceSidebarWidth();
            server.publish("sidebar", JSON.stringify({ type: "re-identify" }));
          }, 300);
        }, 500);
      }
    }
  }

  // Floor configured width against content — widen if saved config is too narrow.
  // Runs immediately (with whatever data is available) and again after 2s once
  // watchers have detected agents, since agent names affect the minimum width.
  function floorWidthToContent() {
    if (!lastState) return;
    const minWidth = computeMinSidebarWidth(lastState.sessions);
    if (configuredWidth >= minWidth) return;
    log("bootstrap", `width ${configuredWidth} < content min ${minWidth}, bumping`);
    configuredWidth = minWidth;
    saveConfig({ sidebarWidth: configuredWidth });
    if (sidebarVisible) enforceSidebarWidth();
    broadcastState();
  }

  broadcastState();
  floorWidthToContent();
  startPaneScan();
  // Run initial pane scan
  refreshPaneAgents();

  // Start agent watchers after server is ready
  for (const w of allWatchers) {
    w.start(watcherCtx);
    log("server", `agent watcher started: ${w.name}`);
  }

  // Recheck after watchers have had time to detect agents
  setTimeout(floorWidthToContent, 2000);

  process.on("SIGINT", () => { cleanup(); process.exit(0); });
  process.on("SIGTERM", () => { cleanup(); process.exit(0); });

  const names = allProviders.map((p) => p.name).join(", ");
  console.log(`tcm server listening on ${SERVER_HOST}:${SERVER_PORT} (mux: ${names})`);
}
