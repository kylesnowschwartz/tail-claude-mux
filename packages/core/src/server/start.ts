import { startServer } from "./index";
import { PluginLoader } from "../plugins/loader";
import { loadConfig } from "../config";
import { AmpAgentWatcher } from "../agents/watchers/amp";
import { ClaudeCodeAgentWatcher } from "../agents/watchers/claude-code";
import { OpenCodeAgentWatcher } from "../agents/watchers/opencode";
import { join } from "path";

const config = loadConfig();
const loader = new PluginLoader();

// 1. Load mux plugins (live outside core, register via plugin factory)
for (const pkg of ["@opensessions/mux-tmux", "@opensessions/mux-zellij"]) {
  try {
    const mod = require(pkg);
    const factory = typeof mod.default === "function" ? mod.default : mod;
    factory({ registerMux: (p: any) => loader.registerMux(p), serverPort: 7391, serverHost: "127.0.0.1" });
  } catch {
    // Plugin not installed — skip
  }
}

// 2. Load local plugins from ~/.config/opensessions/plugins/
const home = process.env.HOME ?? process.env.USERPROFILE ?? "";
const pluginDir = join(home, ".config", "opensessions", "plugins");
const localPlugins = loader.loadDir(pluginDir);
if (localPlugins.length > 0) {
  console.log(`Loaded local plugins: ${localPlugins.join(", ")}`);
}

// 3. Load npm packages from config
if (config.plugins.length > 0) {
  const npmPlugins = loader.loadPackages(config.plugins);
  if (npmPlugins.length > 0) {
    console.log(`Loaded npm plugins: ${npmPlugins.join(", ")}`);
  }
}

// 4. Resolve primary mux provider (config override → env auto-detect)
const mux = loader.resolve(config.mux);
if (!mux) {
  console.error(
    "No terminal multiplexer detected.\n" +
    `Registered providers: ${loader.registry.list().join(", ") || "(none)"}\n` +
    "Are you running inside tmux or zellij?\n" +
    "Set 'mux' in ~/.config/opensessions/config.json to override.",
  );
  process.exit(1);
}

// 5. Gather all OTHER registered providers as extras (multi-mux support)
const extraProviders = loader.registry.list()
  .filter((name) => name !== mux.name)
  .map((name) => loader.registry.get(name)!)
  .filter(Boolean);

if (extraProviders.length > 0) {
  console.log(`Extra mux providers: ${extraProviders.map((p) => p.name).join(", ")}`);
}

// 6. Register built-in agent watchers
loader.registerWatcher(new AmpAgentWatcher());
loader.registerWatcher(new ClaudeCodeAgentWatcher());
loader.registerWatcher(new OpenCodeAgentWatcher());

const watchers = loader.getWatchers();
if (watchers.length > 0) {
  console.log(`Agent watchers: ${watchers.map((w) => w.name).join(", ")}`);
}

console.log(`Primary mux provider: ${mux.name}`);
startServer(mux, extraProviders, watchers);
