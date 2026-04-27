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

/** Plugin entry point for tcm plugin loader */
export default function (api: { registerMux: (p: any) => void }): void {
  api.registerMux(createTmux());
}

export { TmuxProvider, type TmuxProviderSettings } from "./provider";
export { TmuxClient } from "./client";
