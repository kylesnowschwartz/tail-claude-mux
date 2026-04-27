import { TmuxProvider } from "./provider";

/**
 * Create a tmux mux provider.
 *
 * @example
 * ```ts
 * import { createTmux } from "@tcm/mux-tmux";
 * const provider = createTmux();
 * ```
 */
export function createTmux() {
  return new TmuxProvider();
}

export { TmuxProvider } from "./provider";
export { TmuxClient } from "./client";
