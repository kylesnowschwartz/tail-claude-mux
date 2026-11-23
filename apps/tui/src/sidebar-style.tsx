/**
 * Status colours and the closing block rule shared across the sidebar.
 */

import type { Accessor } from "solid-js";
import type { ThemePalette } from "@tcm/runtime";
import type { StatusLabel } from "./attention";

export function statusColor(label: StatusLabel, p: ThemePalette): string {
  if (label === "working") return p.blue;
  if (label === "waiting") return p.yellow;
  if (label === "ready") return p.green;
  if (label === "error") return p.red;
  return p.surface2;
}

/** Closing rule that sets a top block apart from what follows. */
export function BlockRule(props: { palette: Accessor<ThemePalette> }) {
  return (
    <box height={1} flexShrink={0} paddingLeft={1} paddingRight={1}>
      <text wrapMode="none" style={{ fg: props.palette().surface2 }}>{"─".repeat(200)}</text>
    </box>
  );
}
