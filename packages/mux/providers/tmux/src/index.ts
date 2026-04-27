import { TmuxProvider, type TmuxProviderSettings } from "./provider";

/**
 * Create a tmux mux provider.
 *
 * @example
 * ```ts
 * import { createTmux } from "@tcm/mux-tmux";
 * const provider = createTmux();
 * ```
 */
export function createTmux(settings?: TmuxProviderSettings) {
  return new TmuxProvider(settings);
}

export { TmuxProvider, type TmuxProviderSettings } from "./provider";
export { TmuxClient } from "./client";
