import type { MuxProvider } from "../contracts/mux";

/**
 * Registry for MuxProvider implementations.
 *
 * The server resolves which provider to use via:
 *   1. Explicit config override (user picks a mux by name)
 *   2. Auto-detect from env ($TMUX)
 *   3. First registered provider as fallback
 */
export class MuxRegistry {
  private providers = new Map<string, MuxProvider>();

  register(provider: MuxProvider): void {
    this.providers.set(provider.name, provider);
  }

  get(name: string): MuxProvider | null {
    return this.providers.get(name) ?? null;
  }

  list(): string[] {
    return [...this.providers.keys()];
  }

  /**
   * Resolve the active MuxProvider.
   * @param preference — explicit mux name from config. Takes priority.
   * @returns the resolved provider, or null if none found.
   */
  resolve(preference?: string): MuxProvider | null {
    // 1. Explicit preference
    if (preference) {
      return this.providers.get(preference) ?? null;
    }

    // 2. Auto-detect from environment
    if (process.env.TMUX && this.providers.has("tmux")) {
      return this.providers.get("tmux")!;
    }

    // 3. No match
    return null;
  }
}
