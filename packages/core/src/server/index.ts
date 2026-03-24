import { existsSync, readFileSync, unlinkSync, writeFileSync, appendFileSync, watch, type FSWatcher } from "fs";
import { join } from "path";
import type { MuxProvider } from "../contracts/mux";
import type { AgentEvent } from "../contracts/agent";
import { AgentTracker } from "../agents/tracker";
import { SessionOrder } from "./session-order";
import { loadConfig, saveConfig } from "../config";
import { TmuxClient } from "@opensessions/tmux-sdk";
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
  EVENTS_FILE,
} from "../shared";

// --- Debug logger ---

const DEBUG_LOG = "/tmp/opensessions-debug.log";

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

// --- Events file fallback ---

let eventsFileSize = 0;

function readEventsFileFallback(tracker: AgentTracker): void {
  try {
    if (!existsSync(EVENTS_FILE)) return;
    const content = readFileSync(EVENTS_FILE, "utf-8");
    if (content.length <= eventsFileSize) return;
    const newContent = content.slice(eventsFileSize);
    eventsFileSize = content.length;
    for (const line of newContent.split("\n")) {
      if (!line.trim()) continue;
      try {
        const event = JSON.parse(line) as AgentEvent;
        if (event.session && event.status) tracker.applyEvent(event);
      } catch {}
    }
  } catch {}
}

// --- Server startup ---

export function startServer(mux: MuxProvider, extraProviders?: MuxProvider[]): void {
  const allProviders = [mux, ...(extraProviders ?? [])];
  const tracker = new AgentTracker();
  const sessionOrder = new SessionOrder();
  const sdk = new TmuxClient();

  // Clear previous log on server start
  try { writeFileSync(DEBUG_LOG, ""); } catch {}
  log("server", "starting", { providers: allProviders.map((p) => p.name) });

  // Load initial theme from config
  const config = loadConfig();
  let currentTheme: string | undefined = typeof config.theme === "string" ? config.theme : undefined;
  let sidebarWidth = config.sidebarWidth ?? 26;
  let sidebarPosition: "left" | "right" = config.sidebarPosition ?? "left";
  let sidebarVisible = false;

  // scriptsDir is resolved from the OPENSESSIONS_DIR env var or fallback
  const scriptsDir = (() => {
    const envDir = process.env.OPENSESSIONS_DIR;
    if (envDir) return join(envDir, "tmux-plugin", "scripts");
    // Fallback: relative to this file
    return join(import.meta.dir, "..", "..", "..", "tmux-plugin", "scripts");
  })();

  log("server", "config loaded", {
    sidebarWidth, sidebarPosition, scriptsDir,
    theme: currentTheme, configKeys: Object.keys(config),
  });

  // Bootstrap active sessions
  const currentSession = mux.getCurrentSession();
  if (currentSession) {
    tracker.setActiveSessions([currentSession]);
  }

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
    const result = mux.getCurrentSession();
    if (!result) {
      // Fallback: try direct tmux command (works even from background processes)
      const out = sdk.run(["list-clients", "-F", "#{client_session}"]);
      const first = out.stdout.split("\n")[0]?.trim() || null;
      log("getCurrentSession", "mux returned null, fallback", { fallback: first });
      return first;
    }
    log("getCurrentSession", "result", { result });
    return result;
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

    // Sync custom ordering with current session list
    sessionOrder.sync(allMuxSessions.map((s) => s.name));

    // Apply custom ordering
    const orderedNames = sessionOrder.apply(allMuxSessions.map((s) => s.name));
    const sessionByName = new Map(allMuxSessions.map((s) => [s.name, s]));
    const orderedMuxSessions = orderedNames.map((n) => sessionByName.get(n)!);

    // Batch pane counts per provider
    const paneCountMaps = new Map<MuxProvider, Map<string, number>>();
    for (const p of allProviders) {
      if ("getAllPaneCounts" in p && typeof (p as any).getAllPaneCounts === "function") {
        paneCountMaps.set(p, (p as any).getAllPaneCounts());
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
      };
    });

    if (sessions.length === 0) {
      focusedSession = null;
    } else if (!focusedSession || !sessions.some((s) => s.name === focusedSession)) {
      focusedSession = sessions[0]!.name;
    }

    return { type: "state", sessions, focusedSession, currentSession: getCurrentSession(), theme: currentTheme, sidebarWidth, ts: Date.now() };
  }

  function broadcastState() {
    readEventsFileFallback(tracker);
    tracker.pruneStuck(STUCK_RUNNING_TIMEOUT_MS);
    lastState = computeState();
    syncGitWatchers(lastState.sessions, broadcastState);
    const msg = JSON.stringify(lastState);
    server.publish("sidebar", msg);
  }

  function broadcastFocusOnly(sender?: any) {
    if (!lastState) return;
    const currentSession = getCurrentSession();
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
    const hadUnseen = tracker.handleFocus(name);
    if (hadUnseen) {
      broadcastState();
    } else {
      broadcastFocusOnly();
    }
  }

  // --- Sidebar management ---

  function getProviderWithSidebar(): MuxProvider & {
    listSidebarPanes: NonNullable<MuxProvider["listSidebarPanes"]>;
    spawnSidebar: NonNullable<MuxProvider["spawnSidebar"]>;
    hideSidebar: NonNullable<MuxProvider["hideSidebar"]>;
    killSidebarPane: NonNullable<MuxProvider["killSidebarPane"]>;
    resizeSidebarPane: NonNullable<MuxProvider["resizeSidebarPane"]>;
  } | null {
    for (const p of allProviders) {
      if (p.listSidebarPanes && p.spawnSidebar && p.hideSidebar && p.killSidebarPane && p.resizeSidebarPane) {
        return p as any;
      }
    }
    return null;
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

  let spawningInProgress = false;

  function toggleSidebar(ctx?: { session: string; windowId: string }): void {
    const p = getProviderWithSidebar();
    if (!p) {
      log("toggle", "SKIP — no provider with sidebar methods");
      return;
    }

    if (sidebarVisible) {
      const panes = p.listSidebarPanes();
      log("toggle", "OFF — hiding panes (break-pane)", { count: panes.length, panes: panes.map((x) => x.paneId) });
      for (const pane of panes) {
        p.hideSidebar(pane.paneId);
      }
      sidebarVisible = false;
    } else {
      sidebarVisible = true;
      // Spawn/restore sidebar in ALL sessions' active windows at once (one entity)
      const allWindows = sdk.listWindows();
      const activeWindows = allWindows.filter((w) => w.active && w.sessionName !== "_os_stash");
      log("toggle", "ON — spawning in all active windows", { count: activeWindows.length });
      for (const w of activeWindows) {
        ensureSidebarInWindow(p, { session: w.sessionName, windowId: w.id });
      }
      // Enforce correct width on all restored/spawned panes
      resizeSidebars();
      // Tell all TUIs to re-identify (panes may have moved between sessions)
      server.publish("sidebar", JSON.stringify({ type: "re-identify" }));
    }
    log("toggle", "done", { sidebarVisible });
  }

  function ensureSidebarInWindow(p?: ReturnType<typeof getProviderWithSidebar>, ctx?: { session: string; windowId: string }): void {
    p = p ?? getProviderWithSidebar();
    if (!p || !sidebarVisible) {
      log("ensure", "SKIP", { hasProvider: !!p, sidebarVisible });
      return;
    }
    if (spawningInProgress) {
      log("ensure", "SKIP — spawn already in progress (re-entrant guard)");
      return;
    }

    // Use provided context, fall back to querying tmux (unreliable from background)
    const curSession = ctx?.session ?? getCurrentSession();
    if (!curSession) {
      log("ensure", "SKIP — no current session");
      return;
    }

    const windowId = ctx?.windowId ?? sdk.getCurrentWindowId();
    if (!windowId) {
      log("ensure", "SKIP — could not get window_id");
      return;
    }

    const existingPanes = p.listSidebarPanes(curSession);
    const hasInWindow = existingPanes.some((ep) => ep.windowId === windowId);
    log("ensure", "checking window", {
      curSession, windowId, existingPanes: existingPanes.length,
      hasInWindow, paneIds: existingPanes.map((x) => `${x.paneId}@${x.windowId}`),
    });

    if (!hasInWindow) {
      spawningInProgress = true;
      log("ensure", "SPAWNING sidebar", { curSession, windowId, sidebarWidth, sidebarPosition, scriptsDir });
      try {
        const newPaneId = p.spawnSidebar(curSession, windowId, sidebarWidth, sidebarPosition, scriptsDir);
        log("ensure", "spawn result", { newPaneId });
      } finally {
        spawningInProgress = false;
      }
      // Enforce width on ALL sidebar panes after spawn (layout may have adjusted sizes)
      resizeSidebars();
    }
  }

  function quitAll(): void {
    log("quit", "killing all sidebar panes");
    const p = getProviderWithSidebar();
    if (p) {
      const panes = p.listSidebarPanes();
      log("quit", "found panes to kill", { count: panes.length });
      for (const pane of panes) {
        p.killSidebarPane(pane.paneId);
      }
    }
    // Kill the stash session (holds hidden sidebar panes)
    sdk.run(["kill-session", "-t", "_os_stash"]);
    // Broadcast quit to all TUI clients
    server.publish("sidebar", JSON.stringify({ type: "quit" }));
    sidebarVisible = false;
    // Clean up and exit
    cleanup();
    process.exit(0);
  }

  // --- Sidebar resize enforcement ---

  function resizeSidebars() {
    // Enforce sidebar width on all panes (after terminal resize, etc.)
    const p = getProviderWithSidebar();
    if (p) {
      const panes = p.listSidebarPanes();
      log("resize", "enforcing width on all panes", { sidebarWidth, count: panes.length });
      for (const pane of panes) {
        p.resizeSidebarPane(pane.paneId, sidebarWidth);
      }
    }
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
        break;
      }
      case "switch-index": {
        if (!lastState) break;
        const idx = cmd.index - 1;
        if (idx >= 0 && idx < lastState.sessions.length) {
          const name = lastState.sessions[idx]!.name;
          const p = sessionProviders.get(name) ?? mux;
          p.switchSession(name);
        }
        break;
      }
      case "new-session":
        mux.createSession();
        broadcastState();
        break;
      case "kill-session": {
        const p = sessionProviders.get(cmd.name) ?? mux;
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
      case "set-theme":
        currentTheme = cmd.theme;
        saveConfig({ theme: cmd.theme });
        broadcastState();
        break;
      case "report-width":
        // No-op: sidebar width is config-only, not auto-saved from drag
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
    }
  }

  function cleanup() {
    if (debounceTimer) clearTimeout(debounceTimer);
    for (const watcher of gitHeadWatchers.values()) watcher.close();
    gitHeadWatchers.clear();
    if (idleTimer) clearTimeout(idleTimer);
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

      if (req.method === "POST" && url.pathname === "/event") {
        try {
          const body = await req.json() as any;
          if (body.session && body.status) {
            tracker.applyEvent(body as AgentEvent);
            broadcastState();
          }
        } catch {}
        return new Response("ok", { status: 200 });
      }

      if (req.method === "POST" && url.pathname === "/refresh") {
        broadcastState();
        return new Response("ok", { status: 200 });
      }

      if (req.method === "POST" && url.pathname === "/resize-sidebars") {
        log("http", "POST /resize-sidebars", { sidebarWidth });
        resizeSidebars();
        return new Response("ok", { status: 200 });
      }

      if (req.method === "POST" && url.pathname === "/focus") {
        try {
          const body = await req.text();
          const ctx = parseContext(body);
          if (ctx) {
            handleFocus(ctx.session);
          } else {
            // Legacy: body is just the session name
            const name = body.trim().replace(/^"+|"+$/g, "");
            if (name) handleFocus(name);
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

      if (req.method === "POST" && url.pathname === "/ensure-sidebar") {
        try {
          const body = await req.text();
          const ctx = parseContext(body) ?? undefined;
          log("http", "POST /ensure-sidebar", { sidebarVisible, ctx });
          ensureSidebarInWindow(undefined, ctx);
        } catch {}
        return new Response("ok", { status: 200 });
      }

      if (server.upgrade(req, { data: {} })) return;
      return new Response("opensessions server", { status: 200 });
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

  for (const p of allProviders) p.setupHooks(SERVER_HOST, SERVER_PORT);
  broadcastState();

  process.on("SIGINT", () => { cleanup(); process.exit(0); });
  process.on("SIGTERM", () => { cleanup(); process.exit(0); });

  const names = allProviders.map((p) => p.name).join(", ");
  console.log(`opensessions server listening on ${SERVER_HOST}:${SERVER_PORT} (mux: ${names})`);
}
