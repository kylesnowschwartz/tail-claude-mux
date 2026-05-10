// Hook contract snapshot.
//
// Three lockstep copies of "the tmux hook list" exist in the repo:
//   1. integrations/tmux-plugin/scripts/install-hooks.sh   (declarative install)
//   2. integrations/tmux-plugin/scripts/uninstall.sh        (declarative uninstall)
//   3. EXPECTED_TMUX_GLOBAL_HOOKS / EXPECTED_TMUX_WINDOW_HOOKS
//      in packages/runtime/src/server/index.ts              (verifier list)
//
// The runtime's verifyTmuxHooksInstalled() (index.ts) only checks "is the hook
// name set with some body containing :7391/?" — it does NOT enforce that each
// hook points at the right endpoint. Drift between the three copies (e.g. a new
// hook added to install but not uninstall, or a new endpoint added to install
// but not the verifier list) would slip past at runtime.
//
// This test parses install-hooks.sh and uninstall.sh and asserts the literal
// hook names + scopes + endpoint paths line up with the runtime's expected set.
// Added in response to Codex code-review F3 / coverage gap #3.

import { describe, test, expect } from "bun:test";
import { readFileSync } from "fs";
import { join } from "path";

const REPO_ROOT = join(import.meta.dir, "..", "..", "..");
const INSTALL_PATH = join(REPO_ROOT, "integrations/tmux-plugin/scripts/install-hooks.sh");
const UNINSTALL_PATH = join(REPO_ROOT, "integrations/tmux-plugin/scripts/uninstall.sh");

interface ParsedHook {
  name: string;
  scope: "global" | "window-global";
  endpoints: string[]; // paths, in firing order. e.g. ["/focus", "/ensure-sidebar"]
}

/** Pull `tmux set-hook -g <name> "...$BODY..."` invocations out of install-hooks.sh.
 *  Returns each hook's name, scope, and the list of `/path` segments referenced
 *  inside its body (curl POST URLs). Order in the body matters for the chained
 *  client-session-changed hook ("$FOCUS_CMD ; $ENSURE_CMD"). */
function parseInstallHooks(text: string): ParsedHook[] {
  const hooks: ParsedHook[] = [];

  // Resolve the variable lookup table first so we know what each $CMD maps to.
  const varMap = new Map<string, string[]>();
  for (const m of text.matchAll(/^([A-Z_]+_CMD)="\$\(post_(no_data|with_data) (\/[^\s)"']+)/gm)) {
    const [, varName, , path] = m;
    if (varName && path) varMap.set(varName, [path]);
  }

  const setHookRe = /^tmux set-hook (-gw?|-g) +(\S+)\s+"([^"]*)"/gm;
  for (const m of text.matchAll(setHookRe)) {
    const [, scopeFlag, name, body] = m;
    if (!name || !body) continue;
    const endpoints: string[] = [];
    // Body is either a single $VAR or "$VAR1 ; $VAR2". Split on `;` and resolve.
    for (const part of body.split(";").map((p) => p.trim())) {
      const varRef = part.match(/^\$([A-Z_]+_CMD)$/);
      if (!varRef) continue;
      const paths = varMap.get(varRef[1]!);
      if (paths) endpoints.push(...paths);
    }
    hooks.push({
      name,
      scope: scopeFlag === "-gw" ? "window-global" : "global",
      endpoints,
    });
  }

  return hooks;
}

/** Pull `tmux set-hook -gu <name>` / `-guw <name>` invocations out of uninstall.sh. */
function parseUninstallHooks(text: string): { global: Set<string>; windowGlobal: Set<string> } {
  const global = new Set<string>();
  const windowGlobal = new Set<string>();
  // The for-loop forms list hook names on continuation lines:
  //   for hook in \
  //     client-session-changed \
  //     ...
  //     after-kill-pane; do
  //     tmux set-hook -gu "$hook"
  //   done
  const globalLoop = text.match(/for hook in[^]*?do\s+tmux set-hook -gu "\$hook"/);
  if (globalLoop) {
    for (const line of globalLoop[0].split("\n")) {
      const m = line.match(/^\s+([a-z-]+)(?:\s*;\s*do\b)?\s*\\?\s*$/);
      if (m && m[1] && m[1] !== "do") global.add(m[1]);
    }
  }
  const windowLoop = text.match(/for whook in[^]*?do\s+tmux set-hook -guw "\$whook"/);
  if (windowLoop) {
    for (const line of windowLoop[0].split("\n")) {
      const m = line.match(/^\s+([a-z-]+)(?:\s*;\s*do\b)?\s*\\?\s*$/);
      if (m && m[1] && m[1] !== "do") windowGlobal.add(m[1]);
    }
  }
  return { global, windowGlobal };
}

describe("tmux hook contract — install-hooks.sh ↔ uninstall.sh ↔ EXPECTED_TMUX_*_HOOKS", () => {
  const installText = readFileSync(INSTALL_PATH, "utf-8");
  const uninstallText = readFileSync(UNINSTALL_PATH, "utf-8");
  const installed = parseInstallHooks(installText);
  const uninstalled = parseUninstallHooks(uninstallText);

  // Snapshot: keep this list in sync with EXPECTED_TMUX_GLOBAL_HOOKS /
  // EXPECTED_TMUX_WINDOW_HOOKS in packages/runtime/src/server/index.ts.
  // If you're adding a new hook, the failing assertion below tells you which
  // file to edit alongside this snapshot.
  const EXPECTED: ParsedHook[] = [
    { name: "client-session-changed", scope: "global",        endpoints: ["/focus", "/ensure-sidebar"] },
    { name: "session-created",        scope: "global",        endpoints: ["/refresh"] },
    { name: "session-closed",         scope: "global",        endpoints: ["/refresh"] },
    { name: "after-select-window",    scope: "global",        endpoints: ["/ensure-sidebar"] },
    { name: "after-new-window",       scope: "global",        endpoints: ["/ensure-sidebar"] },
    { name: "client-resized",         scope: "global",        endpoints: ["/client-resized"] },
    { name: "after-kill-pane",        scope: "global",        endpoints: ["/pane-exited"] },
    { name: "pane-exited",            scope: "window-global", endpoints: ["/pane-exited"] },
    { name: "pane-focus-in",          scope: "window-global", endpoints: ["/pane-focus"] },
  ];

  test("install-hooks.sh installs every expected hook with the right endpoint(s)", () => {
    expect(installed).toEqual(EXPECTED);
  });

  test("uninstall.sh removes every hook that install-hooks.sh installs (lockstep)", () => {
    const installedGlobal = new Set(EXPECTED.filter((h) => h.scope === "global").map((h) => h.name));
    const installedWindow = new Set(EXPECTED.filter((h) => h.scope === "window-global").map((h) => h.name));
    expect(uninstalled.global).toEqual(installedGlobal);
    expect(uninstalled.windowGlobal).toEqual(installedWindow);
  });

  test("EXPECTED_TMUX_GLOBAL_HOOKS / EXPECTED_TMUX_WINDOW_HOOKS reference matches the install set", () => {
    // Extract the literal arrays from index.ts source. Both arrays are
    // declared `as const`; we don't import them to avoid pulling in
    // bun-server runtime side-effects.
    const indexTs = readFileSync(join(REPO_ROOT, "packages/runtime/src/server/index.ts"), "utf-8");
    const globalMatch = indexTs.match(/EXPECTED_TMUX_GLOBAL_HOOKS\s*=\s*\[([^\]]+)\]/);
    const windowMatch = indexTs.match(/EXPECTED_TMUX_WINDOW_HOOKS\s*=\s*\[([^\]]+)\]/);
    expect(globalMatch).not.toBeNull();
    expect(windowMatch).not.toBeNull();

    const parseList = (raw: string) =>
      new Set(
        raw
          .split(",")
          .map((s) => s.replace(/["\s]/g, ""))
          .filter(Boolean),
      );

    const globalRuntime = parseList(globalMatch![1]!);
    const windowRuntime = parseList(windowMatch![1]!);
    const installedGlobal = new Set(EXPECTED.filter((h) => h.scope === "global").map((h) => h.name));
    const installedWindow = new Set(EXPECTED.filter((h) => h.scope === "window-global").map((h) => h.name));
    expect(globalRuntime).toEqual(installedGlobal);
    expect(windowRuntime).toEqual(installedWindow);
  });
});
