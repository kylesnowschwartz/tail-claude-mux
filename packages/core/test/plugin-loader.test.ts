import { describe, test, expect, beforeEach } from "bun:test";
import { PluginLoader, type PluginAPI } from "../src/plugins/loader";
import type { MuxProvider } from "../src/contracts/mux";
import type { AgentWatcher } from "../src/contracts/agent-watcher";
import { join } from "path";
import { mkdirSync, writeFileSync, rmSync } from "fs";

function fakeMux(name: string): MuxProvider {
  return {
    name,
    listSessions: () => [],
    switchSession: () => {},
    getCurrentSession: () => null,
    getSessionDir: () => "",
    getPaneCount: () => 1,
    getClientTty: () => "",
    createSession: () => {},
    killSession: () => {},
    setupHooks: () => {},
    cleanupHooks: () => {},
  };
}

describe("PluginLoader", () => {
  test("registerMux registers a provider", () => {
    const loader = new PluginLoader();
    loader.registerMux(fakeMux("tmux"));
    expect(loader.registry.list()).toContain("tmux");
  });

  test("registerMux adds a community provider", () => {
    const loader = new PluginLoader();
    loader.registerMux(fakeMux("zellij"));
    expect(loader.registry.list()).toContain("zellij");
  });

  test("resolve with no config uses auto-detect", () => {
    const loader = new PluginLoader();
    loader.registerMux(fakeMux("tmux"));
    const mux = loader.resolve();
    if (process.env.TMUX) {
      expect(mux?.name).toBe("tmux");
    } else {
      expect(mux).toBeNull();
    }
  });

  test("resolve with explicit mux override", () => {
    const loader = new PluginLoader();
    loader.registerMux(fakeMux("tmux"));
    loader.registerMux(fakeMux("zellij"));
    const mux = loader.resolve("zellij");
    expect(mux?.name).toBe("zellij");
  });

  test("resolve returns null for unregistered override", () => {
    const loader = new PluginLoader();
    loader.registerMux(fakeMux("tmux"));
    expect(loader.resolve("screen")).toBeNull();
  });

  test("loadPackages skips missing npm packages gracefully", () => {
    const loader = new PluginLoader();
    const loaded = loader.loadPackages(["opensessions-mux-nonexistent-xyz"]);
    expect(loaded).toEqual([]);
  });

  test("getSetupInfo returns structured setup information", () => {
    const loader = new PluginLoader();
    loader.registerMux(fakeMux("tmux"));
    const info = loader.getSetupInfo();
    expect(info.registeredMuxProviders).toContain("tmux");
    expect(typeof info.configPath).toBe("string");
    expect(info.serverPort).toBe(7391);
  });

  test("registerWatcher stores and retrieves watchers", () => {
    const loader = new PluginLoader();
    const fakeWatcher: AgentWatcher = { name: "amp", start: () => {}, stop: () => {} };
    loader.registerWatcher(fakeWatcher);
    expect(loader.getWatchers()).toHaveLength(1);
    expect(loader.getWatchers()[0]!.name).toBe("amp");
  });

  test("registerWatcher supports multiple watchers", () => {
    const loader = new PluginLoader();
    loader.registerWatcher({ name: "amp", start: () => {}, stop: () => {} });
    loader.registerWatcher({ name: "claude-code", start: () => {}, stop: () => {} });
    expect(loader.getWatchers()).toHaveLength(2);
    expect(loader.getWatchers().map((w) => w.name)).toEqual(["amp", "claude-code"]);
  });
});

describe("PluginLoader — factory loading from directory", () => {
  const tmpDir = `/tmp/opensessions-plugin-test-${Date.now()}`;
  const pluginDir = join(tmpDir, "plugins");

  beforeEach(() => {
    rmSync(tmpDir, { recursive: true, force: true });
    mkdirSync(pluginDir, { recursive: true });
  });

  test("loadDir loads a .ts plugin that exports default factory", () => {
    // Write a plugin that registers a mux provider via the API
    writeFileSync(
      join(pluginDir, "fake-mux.ts"),
      `export default function(api) {
        api.registerMux({
          name: "fake-from-file",
          listSessions: () => [],
          switchSession: () => {},
          getCurrentSession: () => null,
          getSessionDir: () => "",
          getPaneCount: () => 1,
          getClientTty: () => "",
          createSession: () => {},
          killSession: () => {},
          setupHooks: () => {},
          cleanupHooks: () => {},
        });
      }`,
    );

    const loader = new PluginLoader();
    const loaded = loader.loadDir(pluginDir);
    expect(loaded).toContain("fake-mux.ts");
    expect(loader.registry.list()).toContain("fake-from-file");
  });

  test("loadDir loads plugin from subdirectory with index.ts", () => {
    const subDir = join(pluginDir, "my-plugin");
    mkdirSync(subDir, { recursive: true });
    writeFileSync(
      join(subDir, "index.ts"),
      `export default function(api) {
        api.registerMux({
          name: "sub-plugin",
          listSessions: () => [],
          switchSession: () => {},
          getCurrentSession: () => null,
          getSessionDir: () => "",
          getPaneCount: () => 1,
          getClientTty: () => "",
          createSession: () => {},
          killSession: () => {},
          setupHooks: () => {},
          cleanupHooks: () => {},
        });
      }`,
    );

    const loader = new PluginLoader();
    const loaded = loader.loadDir(pluginDir);
    expect(loaded).toContain("my-plugin");
    expect(loader.registry.list()).toContain("sub-plugin");
  });

  test("loadDir skips non-ts/js files", () => {
    writeFileSync(join(pluginDir, "readme.md"), "# Not a plugin");
    writeFileSync(
      join(pluginDir, "real.ts"),
      `export default function(api) {}`,
    );

    const loader = new PluginLoader();
    const loaded = loader.loadDir(pluginDir);
    expect(loaded).toEqual(["real.ts"]);
  });

  test("loadDir handles broken plugins gracefully", () => {
    writeFileSync(
      join(pluginDir, "broken.ts"),
      `export default function(api) { throw new Error("boom"); }`,
    );

    const loader = new PluginLoader();
    const loaded = loader.loadDir(pluginDir);
    expect(loaded).toEqual([]);
  });

  test("loadDir returns empty for nonexistent directory", () => {
    const loader = new PluginLoader();
    const loaded = loader.loadDir("/tmp/does-not-exist-" + Date.now());
    expect(loaded).toEqual([]);
  });

  test("PluginAPI shape matches expected contract", () => {
    let receivedApi: PluginAPI | null = null;
    writeFileSync(
      join(pluginDir, "inspect.ts"),
      `export default function(api) {
        // Just verify the api has the right methods
        if (typeof api.registerMux !== "function") throw new Error("missing registerMux");
        if (typeof api.registerWatcher !== "function") throw new Error("missing registerWatcher");
        if (typeof api.serverPort !== "number") throw new Error("missing serverPort");
        if (typeof api.serverHost !== "string") throw new Error("missing serverHost");
      }`,
    );

    const loader = new PluginLoader();
    const loaded = loader.loadDir(pluginDir);
    expect(loaded).toContain("inspect.ts");
  });
});
