import { ZellijProvider, type ZellijProviderSettings } from "./provider";

/**
 * Create a Zellij mux provider.
 *
 * @example
 * ```ts
 * import { createZellij } from "@tcm/mux-zellij";
 * const provider = createZellij();
 * ```
 */
export function createZellij(settings?: ZellijProviderSettings) {
  return new ZellijProvider(settings);
}

/** Plugin entry point for tcm plugin loader */
export default function (api: { registerMux: (p: any) => void }): void {
  api.registerMux(createZellij());
}

export { ZellijProvider, type ZellijProviderSettings } from "./provider";
