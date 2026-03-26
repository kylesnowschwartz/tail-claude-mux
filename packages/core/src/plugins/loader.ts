import { existsSync, readdirSync, statSync } from "fs";
import { join, extname } from "path";
import type { MuxProvider } from "../contracts/mux";
import type { AgentWatcher } from "../contracts/agent-watcher";
import { MuxRegistry } from "../mux/registry";
import { SERVER_PORT, SERVER_HOST } from "../shared";

/**
 * The API surface passed to every plugin factory function.
 * Inspired by pi-mono's ExtensionAPI pattern:
 *   export default function(api: PluginAPI) { ... }
 */
export interface PluginAPI {
  registerMux(provider: MuxProvider): void;
  registerWatcher(watcher: AgentWatcher): void;
  readonly serverPort: number;
  readonly serverHost: string;
}

/** Plugin factory — the single export a plugin must provide */
export type PluginFactory = (api: PluginAPI) => void | Promise<void>;

export class PluginLoader {
  readonly registry = new MuxRegistry();
  private watchers: AgentWatcher[] = [];

  registerMux(provider: MuxProvider): void {
    this.registry.register(provider);
  }

  registerWatcher(watcher: AgentWatcher): void {
    this.watchers.push(watcher);
  }

  getWatchers(): AgentWatcher[] {
    return [...this.watchers];
  }

  resolve(preference?: string): MuxProvider | null {
    return this.registry.resolve(preference);
  }

  /**
   * Build the PluginAPI object that gets passed to every factory function.
   */
  private createAPI(): PluginAPI {
    return {
      registerMux: (provider: MuxProvider) => this.registry.register(provider),
      registerWatcher: (watcher: AgentWatcher) => this.registerWatcher(watcher),
      serverPort: SERVER_PORT,
      serverHost: SERVER_HOST,
    };
  }

  /**
   * Load plugins from a directory (like ~/.config/opensessions/plugins/).
   * Scans one level deep:
   *   - *.ts / *.js files → loaded directly
   *   - subdirs with index.ts / index.js → loaded as entry point
   *
   * Each must `export default function(api: PluginAPI) { ... }`
   *
   * Returns names of successfully loaded plugins.
   */
  loadDir(dir: string): string[] {
    if (!existsSync(dir)) return [];

    const loaded: string[] = [];
    const api = this.createAPI();
    const entries = readdirSync(dir);

    for (const entry of entries) {
      const fullPath = join(dir, entry);
      const stat = statSync(fullPath);

      if (stat.isFile()) {
        const ext = extname(entry);
        if (ext !== ".ts" && ext !== ".js") continue;
        if (this.loadFactory(fullPath, api)) loaded.push(entry);
      } else if (stat.isDirectory()) {
        // Check for index.ts or index.js
        const indexTs = join(fullPath, "index.ts");
        const indexJs = join(fullPath, "index.js");
        const indexPath = existsSync(indexTs) ? indexTs : existsSync(indexJs) ? indexJs : null;
        if (indexPath && this.loadFactory(indexPath, api)) loaded.push(entry);
      }
    }

    return loaded;
  }

  /**
   * Load community plugins from npm package names.
   * Each package should `export default function(api: PluginAPI) { ... }`
   * or have a package.json "opensessions" field pointing to the entry file.
   *
   * Returns names of successfully loaded packages.
   */
  loadPackages(packageNames: string[]): string[] {
    const loaded: string[] = [];
    const api = this.createAPI();

    for (const pkg of packageNames) {
      try {
        const mod = require(pkg);
        const factory: PluginFactory | undefined =
          typeof mod.default === "function" ? mod.default : typeof mod === "function" ? mod : undefined;
        if (factory) {
          factory(api);
          loaded.push(pkg);
        }
      } catch {
        // Package not installed or broken — skip
      }
    }

    return loaded;
  }

  /**
   * Load a single factory from a file path.
   */
  private loadFactory(filePath: string, api: PluginAPI): boolean {
    try {
      const mod = require(filePath);
      const factory: PluginFactory | undefined =
        typeof mod.default === "function" ? mod.default : typeof mod === "function" ? mod : undefined;
      if (!factory) return false;
      factory(api);
      return true;
    } catch {
      return false;
    }
  }

  getSetupInfo(): { registeredMuxProviders: string[]; configPath: string; serverPort: number } {
    const home = process.env.HOME ?? process.env.USERPROFILE ?? "";
    return {
      registeredMuxProviders: this.registry.list(),
      configPath: join(home, ".config", "opensessions", "config.json"),
      serverPort: SERVER_PORT,
    };
  }
}
