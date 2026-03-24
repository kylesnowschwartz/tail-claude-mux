import type { MuxProvider, MuxSessionInfo } from "@opensessions/core";
import { TmuxClient } from "@opensessions/tmux-sdk";
import { appendFileSync } from "fs";

const tmux = new TmuxClient();

function plog(msg: string, data?: Record<string, unknown>) {
  const ts = new Date().toISOString().slice(11, 23);
  const extra = data ? " " + JSON.stringify(data) : "";
  try { appendFileSync("/tmp/opensessions-debug.log", `[${ts}] [provider] ${msg}${extra}\n`); } catch {}
}

/** Direct tmux call bypassing SDK (SDK has \x1f parsing issues) */
function rawTmux(args: string[]): string {
  try {
    const r = Bun.spawnSync(["tmux", ...args], { stdout: "pipe", stderr: "pipe" });
    return r.stdout.toString().trim();
  } catch { return ""; }
}

const STASH_SESSION = "_os_stash";

export class TmuxProvider implements MuxProvider {
  readonly name = "tmux";

  listSessions(): MuxSessionInfo[] {
    return tmux.listSessions()
      .filter((s) => s.name !== STASH_SESSION)
      .map((s) => ({
        name: s.name,
        createdAt: s.createdAt,
        dir: s.dir,
        windows: s.windowCount,
      }));
  }

  switchSession(name: string, clientTty?: string): void {
    tmux.switchClient(name, clientTty ? { clientTty } : undefined);
  }

  getCurrentSession(): string | null {
    return tmux.getCurrentSession();
  }

  getSessionDir(name: string): string {
    return tmux.getSessionDir(name);
  }

  getPaneCount(name: string): number {
    return tmux.getPaneCount(name);
  }

  getClientTty(): string {
    return tmux.getClientTty();
  }

  createSession(name?: string, dir?: string): void {
    tmux.newSession({ name, cwd: dir });
  }

  killSession(name: string): void {
    tmux.killSession(name);
  }

  setupHooks(serverHost: string, serverPort: number): void {
    const base = `http://${serverHost}:${serverPort}`;
    // tmux expands #{} formats at hook-fire time — no need for $(tmux display-message)
    // Use | as field separator (safe for session names, window IDs, TTYs)
    const focusCmd = `run-shell -b "curl -s -o /dev/null -X POST ${base}/focus -d '#{client_tty}|#{session_name}|#{window_id}'"`;
    const refreshCmd = `run-shell -b "curl -s -o /dev/null -X POST ${base}/refresh"`;
    const resizeCmd = `run-shell -b "curl -s -o /dev/null -X POST ${base}/resize-sidebars"`;
    const ensureCmd = `run-shell -b "curl -s -o /dev/null -X POST ${base}/ensure-sidebar -d '#{client_tty}|#{session_name}|#{window_id}'"`;

    // client-session-changed: update focus AND ensure sidebar in the new session's window
    tmux.setGlobalHook("client-session-changed", `${focusCmd} ; ${ensureCmd}`);
    tmux.setGlobalHook("session-created", refreshCmd);
    tmux.setGlobalHook("session-closed", refreshCmd);
    tmux.setGlobalHook("client-resized", resizeCmd);
    tmux.setGlobalHook("after-select-window", ensureCmd);
    tmux.setGlobalHook("after-new-window", ensureCmd);
  }

  cleanupHooks(): void {
    tmux.unsetGlobalHook("client-session-changed");
    tmux.unsetGlobalHook("session-created");
    tmux.unsetGlobalHook("session-closed");
    tmux.unsetGlobalHook("client-resized");
    tmux.unsetGlobalHook("after-select-window");
    tmux.unsetGlobalHook("after-new-window");
  }

  getAllPaneCounts(): Map<string, number> {
    return tmux.getAllPaneCounts();
  }

  listSidebarPanes(sessionName?: string): { paneId: string; sessionName: string; windowId: string }[] {
    const panes = sessionName
      ? tmux.listPanes({ scope: "session", target: sessionName })
      : tmux.listPanes();

    return panes
      .filter((p) => p.title === "opensessions" && p.sessionName !== STASH_SESSION)
      .map((p) => ({ paneId: p.id, sessionName: p.sessionName, windowId: p.windowId }));
  }

  /** Ensure the invisible stash session exists for hiding sidebar panes */
  private ensureStash(): void {
    const r = Bun.spawnSync(["tmux", "has-session", "-t", STASH_SESSION], { stdout: "pipe", stderr: "pipe" });
    if (r.exitCode !== 0) {
      rawTmux(["new-session", "-d", "-s", STASH_SESSION, "-x", "80", "-y", "24"]);
    }
  }

  spawnSidebar(
    sessionName: string,
    windowId: string,
    width: number,
    position: "left" | "right",
    scriptsDir: string,
  ): string | null {
    // Find the edge pane to split against
    const panes = tmux.listPanes({ scope: "window", target: windowId });
    plog("spawnSidebar", { windowId, paneCount: panes.length });
    if (panes.length === 0) return null;

    const targetPane = position === "left"
      ? panes.reduce((a, b) => (a.left <= b.left ? a : b))
      : panes.reduce((a, b) => (a.right >= b.right ? a : b));

    // --- Try to restore a stashed sidebar pane ---
    try {
      const stashPanes = tmux.listPanes({ scope: "session", target: STASH_SESSION });
      const stashedPane = stashPanes.find((p) => p.title === "opensessions");
      if (stashedPane) {
        plog("spawnSidebar: restoring from stash", { paneId: stashedPane.id, target: targetPane.id });
        const joinFlag = position === "left" ? "-hb" : "-h";
        rawTmux(["join-pane", joinFlag, "-l", String(width), "-s", stashedPane.id, "-t", targetPane.id]);
        tmux.setPaneTitle(stashedPane.id, "opensessions");
        tmux.selectPane(targetPane.id);
        return stashedPane.id;
      }
    } catch { /* stash session doesn't exist yet — spawn fresh */ }

    // --- No stashed pane, spawn fresh ---
    plog("spawnSidebar: spawning new", { target: targetPane.id, width, position });
    const newPane = tmux.splitWindow({
      target: targetPane.id,
      direction: "horizontal",
      before: position === "left",
      size: width,
      command: `REFOCUS_WINDOW=${windowId} exec ${scriptsDir}/start.sh`,
    });

    if (!newPane) {
      plog("spawnSidebar: splitWindow FAILED");
      return null;
    }

    tmux.setPaneTitle(newPane.id, "opensessions");
    tmux.selectPane(targetPane.id);
    return newPane.id;
  }

  hideSidebar(paneId: string): void {
    this.ensureStash();
    // Move pane into invisible stash session (no client attached = not visible anywhere)
    plog("hideSidebar: stashing pane", { paneId });
    rawTmux(["join-pane", "-d", "-s", paneId, "-t", `${STASH_SESSION}:`]);
  }

  killSidebarPane(paneId: string): void {
    tmux.killPane(paneId);
  }

  resizeSidebarPane(paneId: string, width: number): void {
    tmux.resizePane(paneId, { width });
  }
}
