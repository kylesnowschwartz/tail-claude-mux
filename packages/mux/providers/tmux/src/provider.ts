import type {
  MuxProviderV1,
  MuxSessionInfo,
  ActiveWindow,
  SidebarPane,
  SidebarPosition,
  WindowCapable,
  SidebarCapable,
  BatchCapable,
} from "@tcm/mux";
import { TmuxClient } from "./client";
import { appendFileSync } from "fs";

const tmux = new TmuxClient();

function plog(msg: string, data?: Record<string, unknown>) {
  const ts = new Date().toISOString().slice(11, 23);
  const extra = data ? " " + JSON.stringify(data) : "";
  try { appendFileSync("/tmp/tcm-debug.log", `[${ts}] [provider] ${msg}${extra}\n`); } catch {}
}

/** Direct tmux call bypassing SDK (SDK has \x1f parsing issues) */
function rawTmux(args: string[]): string {
  try {
    const r = Bun.spawnSync(["tmux", ...args], { stdout: "pipe", stderr: "pipe" });
    return r.stdout.toString().trim();
  } catch { return ""; }
}

const STASH_SESSION = "_tcm_stash";
const SIDEBAR_PANE_TITLE = "tcm-sidebar";
/** tmux pane-local user-option set on every sidebar pane we spawn. Stable
 *  across our process restarts (tmux owns the storage) and can't be
 *  silently rewritten by anything inside the pane the way `pane_title`
 *  can (shell escape `\\e]2;...\\a` rewrites the title). Identification
 *  primary key. The title is kept as a recovery hint for in-flight
 *  migration. */
const SIDEBAR_MARKER_OPTION = "@tcm-sidebar";
const SIDEBAR_MARKER_VALUE = "1";

/** A pane is a sidebar iff its @tcm-sidebar marker is set, OR (for backward
 *  compat with sidebars spawned before this marker was introduced) its
 *  pane_title equals SIDEBAR_PANE_TITLE. New spawns always set the marker. */
function isSidebarPane(p: { tcmSidebar: string; title: string }): boolean {
  return p.tcmSidebar === SIDEBAR_MARKER_VALUE || p.title === SIDEBAR_PANE_TITLE;
}

export class TmuxProvider implements MuxProviderV1, WindowCapable, SidebarCapable, BatchCapable {
  readonly name = "tmux";

  listSessions(): MuxSessionInfo[] {
    const sessions = tmux.listSessions()
      .filter((s) => s.name !== STASH_SESSION);
    const activeDirs = tmux.getActiveSessionDirs();
    return sessions.map((s) => ({
      name: s.name,
      createdAt: s.createdAt,
      dir: activeDirs.get(s.name) ?? s.dir,
      windows: s.windowCount,
    }));
  }

  switchSession(name: string, clientTty?: string): void {
    tmux.switchClient(name, clientTty ? { clientTty } : undefined);
  }

  getCurrentSession(clientTty?: string): string | null {
    return tmux.getCurrentSession(clientTty);
  }

  listAttachedSessions(): string[] {
    const seen = new Set<string>();
    for (const c of tmux.listClients()) {
      if (c.sessionName) seen.add(c.sessionName);
    }
    return [...seen];
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

  // setupHooks / cleanupHooks were removed in fix/tmux-cold-start-determinism.
  // Hooks are now installed by integrations/tmux-plugin/scripts/install-hooks.sh
  // at TPM init, and uninstalled by integrations/tmux-plugin/scripts/uninstall.sh.
  // Removing them from this provider eliminates the fourth lockstep copy of the
  // hook list (Codex review F3) — the install/uninstall shell scripts plus the
  // EXPECTED_TMUX_*_HOOKS verifier list in packages/runtime/src/server/index.ts
  // are now the only source of truth.

  getAllPaneCounts(): Map<string, number> {
    return tmux.getAllPaneCounts();
  }

  listActiveWindows(): ActiveWindow[] {
    return tmux.listWindows()
      .filter((w) => w.active && w.sessionName !== STASH_SESSION)
      .map((w) => ({ id: w.id, sessionName: w.sessionName, active: w.active }));
  }

  getCurrentWindowId(): string | null {
    return tmux.getCurrentWindowId() || null;
  }

  cleanupSidebar(): void {
    // Kill the stash session used for hiding sidebar panes.
    try {
      Bun.spawnSync(["tmux", "kill-session", "-t", STASH_SESSION], { stdout: "pipe", stderr: "pipe" });
    } catch {}
  }

  /** Prune any pane in the stash session whose title isn't `tcm-sidebar`.
   *  These accumulate when a TUI process exits and tmux's automatic-rename
   *  re-derives the title from the running command (often the user's
   *  hostname). Without this prune they linger forever, taking up stash
   *  space and confusing future spawnSidebar restore attempts.
   *  Runs as a side-effect of every hideSidebar / spawnSidebar restore
   *  attempt and at server startup. */
  pruneStashOrphans(): void {
    let stashPanes;
    try {
      stashPanes = tmux.listPanes({ scope: "session", target: STASH_SESSION });
    } catch {
      return; // stash session doesn't exist — nothing to prune
    }
    for (const p of stashPanes) {
      if (!isSidebarPane(p)) {
        plog("pruneStashOrphans", { paneId: p.id, title: p.title, tcmSidebar: p.tcmSidebar });
        try { tmux.killPane(p.id); } catch {}
      }
    }
  }

  listSidebarPanes(sessionName?: string): SidebarPane[] {
    const panes = sessionName
      ? tmux.listPanes({ scope: "session", target: sessionName })
      : tmux.listPanes();
    const windowWidths = new Map<string, number>();
    for (const pane of panes) {
      windowWidths.set(pane.windowId, Math.max(windowWidths.get(pane.windowId) ?? 0, pane.right + 1));
    }

    return panes
      .filter((p) => isSidebarPane(p) && p.sessionName !== STASH_SESSION)
      .map((p) => ({
        paneId: p.id,
        sessionName: p.sessionName,
        windowId: p.windowId,
        width: p.width,
        windowWidth: windowWidths.get(p.windowId),
      }));
  }

  /** Ensure the invisible stash session exists for hiding sidebar panes */
  private ensureStash(): void {
    const r = Bun.spawnSync(["tmux", "has-session", "-t", STASH_SESSION], { stdout: "pipe", stderr: "pipe" });
    if (r.exitCode !== 0) {
      rawTmux(["new-session", "-d", "-s", STASH_SESSION, "-x", "80", "-y", "24"]);
    }
  }

  spawnSidebar(
    _sessionName: string,
    windowId: string,
    width: number,
    position: SidebarPosition,
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
      const stashedPane = stashPanes.find((p) => isSidebarPane(p));
      if (stashedPane) {
        plog("spawnSidebar: restoring from stash", { paneId: stashedPane.id, target: targetPane.id });
        const joinFlag = position === "left" ? "-hb" : "-h";
        rawTmux(["join-pane", joinFlag, "-f", "-l", String(width), "-s", stashedPane.id, "-t", targetPane.id]);
        tmux.setPaneTitle(stashedPane.id, SIDEBAR_PANE_TITLE);
        // Re-stamp the marker — opportunistic migration for old stashed
        // panes that pre-date the @tcm-sidebar option.
        tmux.setPaneOption(stashedPane.id, SIDEBAR_MARKER_OPTION, SIDEBAR_MARKER_VALUE);
        // Do NOT selectPane here — same as fresh spawns. The TUI's
        // restoreTerminalModes fires on focus-in after join-pane, generating
        // capability query responses. Refocusing the main pane immediately
        // causes those responses to leak as garbage escape sequences.
        return stashedPane.id;
      }
    } catch { /* stash session doesn't exist yet — spawn fresh */ }

    // --- No stashed pane, spawn fresh ---
    plog("spawnSidebar: spawning new", { target: targetPane.id, width, position });
    const newPane = tmux.splitWindow({
      target: targetPane.id,
      direction: "horizontal",
      before: position === "left",
      fullWindow: true,
      size: width,
      command: `REFOCUS_WINDOW=${windowId} exec ${scriptsDir}/start.sh`,
    });

    if (!newPane) {
      plog("spawnSidebar: splitWindow FAILED");
      return null;
    }

    tmux.setPaneTitle(newPane.id, SIDEBAR_PANE_TITLE);
    // Stable identification marker — survives pane_title rewriting by
    // any escape sequences the TUI process emits while running.
    tmux.setPaneOption(newPane.id, SIDEBAR_MARKER_OPTION, SIDEBAR_MARKER_VALUE);
    // Do NOT selectPane here for fresh spawns — the TUI's refocusMainPane()
    // handles it after terminal capability detection finishes. Refocusing
    // immediately causes capability query responses (DECRPM, DA1, Kitty
    // graphics) to be routed to the main pane as garbage escape sequences.
    return newPane.id;
  }

  hideSidebar(paneId: string): void {
    this.ensureStash();
    // Prune orphan panes (titles that drifted away from `tcm-sidebar`)
    // before adding a new one. Stops the stash from accumulating cruft
    // across hide/restore cycles — the previous tcm-sidebar pane whose
    // TUI process exited could end up with the user's hostname as title.
    this.pruneStashOrphans();
    // Ensure the stash window is large enough to accept another pane.
    // join-pane fails with "pane too small" when stash panes fill up.
    rawTmux(["resize-window", "-t", `${STASH_SESSION}:`, "-x", "200", "-y", "200"]);
    plog("hideSidebar: stashing pane", { paneId });
    rawTmux(["join-pane", "-d", "-s", paneId, "-t", `${STASH_SESSION}:`]);
  }

  killSidebarPane(paneId: string): void {
    tmux.killPane(paneId);
  }

  resizeSidebarPane(paneId: string, width: number): void {
    tmux.resizePane(paneId, { width });
  }

  killOrphanedSidebarPanes(): void {
    const allPanes = tmux.listPanes();
    // Count panes per window
    const windowPaneCounts = new Map<string, number>();
    for (const p of allPanes) {
      if (p.sessionName === STASH_SESSION) continue;
      windowPaneCounts.set(p.windowId, (windowPaneCounts.get(p.windowId) ?? 0) + 1);
    }
    // Find sidebar panes that are the only pane in their window
    for (const p of allPanes) {
      if (!isSidebarPane(p) || p.sessionName === STASH_SESSION) continue;
      if (windowPaneCounts.get(p.windowId) === 1) {
        tmux.killPane(p.id);
      }
    }
  }
}
