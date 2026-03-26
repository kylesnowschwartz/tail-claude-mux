import { existsSync, readFileSync, unlinkSync, writeFileSync, appendFileSync, watch, type FSWatcher } from "fs";
import { join } from "path";
import type { MuxProvider } from "../contracts/mux";
import { isFullSidebarCapable, isBatchCapable } from "../contracts/mux";
import type { AgentEvent } from "../contracts/agent";
import type { AgentWatcher, AgentWatcherContext } from "../contracts/agent-watcher";
import { AgentTracker } from "../agents/tracker";
import { SessionOrder } from "./session-order";
import { loadConfig, saveConfig } from "../config";
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

// --- Server startup ---

export function startServer(mux: MuxProvider, extraProviders?: MuxProvider[], watchers?: AgentWatcher[]): void {
  const allProviders = [mux, ...(extraProviders ?? [])];
  const allWatchers = watchers ?? [];
  const tracker = new AgentTracker();
  const home = process.env.HOME ?? process.env.USERPROFILE ?? "";
  const sessionOrderPath = join(home, ".config", "opensessions", "session-order.json");
  const sessionOrder = new SessionOrder(sessionOrderPath);

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

  // --- Start agent watchers ---

  const watcherCtx: AgentWatcherContext = {
    resolveSession(projectDir: string): string | null {
      for (const p of allProviders) {
        for (const s of p.listSessions()) {
          if (!s.dir) continue;
          if (s.dir === projectDir) return s.name;
          if (projectDir.startsWith(s.dir + "/") || s.dir.startsWith(projectDir + "/")) return s.name;
        }
      }
      return null;
    },
    emit(event: AgentEvent) {
      tracker.applyEvent(event);
      broadcastState();
    },
  };

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

    // Sync custom ordering with current session list
    sessionOrder.sync(allMuxSessions.map((s) => s.name));

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
    tracker.pruneStuck(STUCK_RUNNING_TIMEOUT_MS);
    tracker.pruneTerminal();
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

  let spawningInProgress = false;

  function toggleSidebar(ctx?: { session: string; windowId: string }): void {
    const providers = getProvidersWithSidebar();
    if (providers.length === 0) {
      log("toggle", "SKIP — no providers with sidebar methods");
      return;
    }

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
      for (const p of providers) {
        const allWindows = p.listActiveWindows();
        log("toggle", "ON — spawning in active windows", { provider: p.name, count: allWindows.length });
        for (const w of allWindows) {
          ensureSidebarInWindow(p, { session: w.sessionName, windowId: w.id });
        }
      }
      resizeSidebars();
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
    if (spawningInProgress) {
      log("ensure", "SKIP — spawn already in progress");
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
      resizeSidebars();
    }
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

  // --- Sidebar resize enforcement ---

  function resizeSidebars() {
    for (const p of getProvidersWithSidebar()) {
      const panes = p.listSidebarPanes();
      log("resize", "enforcing width on all panes", { provider: p.name, sidebarWidth, count: panes.length });
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

        // Detect cross-mux switch (e.g., zellij→tmux or tmux→zellij)
        const sourceProvider = clientSess ? sessionProviders.get(clientSess) : null;
        if (sourceProvider && sourceProvider.name !== p.name) {
          log("switch-session", "cross-mux detected", {
            source: sourceProvider.name, target: p.name, sourceSession: clientSess,
          });
          if (sourceProvider.name === "zellij" && p.name === "tmux") {
            // Write reattach target for the bash wrapper
            writeFileSync("/tmp/opensessions-reattach", cmd.name);
            // Detach from zellij — the wrapper script will auto-attach to tmux
            Bun.spawnSync(["zellij", "--session", clientSess!, "action", "detach"], {
              stdout: "pipe", stderr: "pipe",
            });
            break; // Don't call p.switchSession — the wrapper handles it
          }
        }

        p.switchSession(cmd.name, tty);

        // Auto-ensure sidebar in the target session if sidebar is visible.
        // In tmux, hooks handle this — but zellij has no hooks, so we do it here.
        // Use listActiveWindows() to find the target session's active tab
        // (getCurrentWindowId() won't work from the server since ZELLIJ_SESSION_NAME isn't set).
        if (sidebarVisible && isFullSidebarCapable(p)) {
          const activeWindows = p.listActiveWindows();
          const targetWindow = activeWindows.find((w) => w.sessionName === cmd.name);
          log("switch-session", "auto-ensure sidebar", {
            target: cmd.name, provider: p.name,
            activeWindows: activeWindows.length, targetWindow: targetWindow?.id ?? null,
          });
          if (targetWindow) {
            // 1.5s delay — zellij needs time to attach the client before we can spawn panes
            setTimeout(() => {
              ensureSidebarInWindow(p, { session: cmd.name, windowId: targetWindow.id });
            }, 1500);
          }
        }
        break;
      }
      case "switch-index": {
        if (!lastState) break;
        const idx = cmd.index - 1;
        if (idx >= 0 && idx < lastState.sessions.length) {
          const name = lastState.sessions[idx]!.name;
          const p = sessionProviders.get(name) ?? mux;
          p.switchSession(name);

          if (sidebarVisible && isFullSidebarCapable(p)) {
            const activeWindows = p.listActiveWindows();
            const targetWindow = activeWindows.find((w) => w.sessionName === name);
            if (targetWindow) {
              setTimeout(() => {
                ensureSidebarInWindow(p, { session: name, windowId: targetWindow.id });
              }, 500);
            }
          }
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
    for (const w of allWatchers) w.stop();
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

  // Start agent watchers after server is ready
  for (const w of allWatchers) {
    w.start(watcherCtx);
    log("server", `agent watcher started: ${w.name}`);
  }

  process.on("SIGINT", () => { cleanup(); process.exit(0); });
  process.on("SIGTERM", () => { cleanup(); process.exit(0); });

  const names = allProviders.map((p) => p.name).join(", ");
  console.log(`opensessions server listening on ${SERVER_HOST}:${SERVER_PORT} (mux: ${names})`);
}
