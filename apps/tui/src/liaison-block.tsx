/**
 * The sidebar's liaison block: the rows pinned at the top for one open
 * liaison, plus the status colours and the closing rule it shares with the
 * rest of the sidebar.
 *
 * The block is dark at rest: one dim title row. Colour appears only for an
 * item that needs the engineer, in the colour of its priority.
 */

import { For, Show, type Accessor } from "solid-js";
import type { LiaisonItem, ThemePalette } from "@tcm/runtime";
import { LIAISON_GLYPH, LIAISON_ITEM_GLYPHS, LIAISON_WORKERS_MARK } from "./vocab";
import { tier } from "./tiers";
import {
  formatAge,
  LIAISON_ITEM_PRIORITY,
  LIAISON_ITEM_ROWS,
  liaisonTitle,
  type SessionAgentRef,
  type StatusLabel,
} from "./attention";

export function statusColor(label: StatusLabel, p: ThemePalette): string {
  if (label === "working") return p.blue;
  if (label === "waiting") return p.yellow;
  if (label === "ready") return p.green;
  if (label === "error") return p.red;
  return p.surface2;
}

/** Colour per item priority: the theme's error, warning, and info colours. */
function priorityColor(priority: 1 | 2 | 3, p: ThemePalette): string {
  return priority === 1 ? p.red : priority === 2 ? p.yellow : p.blue;
}

export interface LiaisonBlockProps {
  liaison: SessionAgentRef;
  palette: Accessor<ThemePalette>;
  nowMs: Accessor<number>;
  /** Items for the engineer, in display order (see liaisonItems). */
  items: Accessor<LiaisonItem[]>;
  /** Count of live conversations this liaison started; `↳N` shows when it is above zero. */
  workers: Accessor<number>;
}

/** One liaison: a dim title row, then a row per item that needs the engineer. */
export function LiaisonBlock(props: LiaisonBlockProps) {
  const P = () => props.palette();
  const title = () => liaisonTitle(props.liaison.agent.threadName ?? "");
  const shown = () => props.items().slice(0, LIAISON_ITEM_ROWS);
  const hidden = () => props.items().length - shown().length;
  const titleStyle = () => {
    const top = props.items()[0];
    return top ? { fg: priorityColor(LIAISON_ITEM_PRIORITY[top.kind], P()) } : tier("dim", P());
  };
  return (
    <box flexDirection="column" flexShrink={0}>
    <box flexDirection="column" flexShrink={0} paddingLeft={1} paddingRight={1}>
      <box flexDirection="row">
        <text flexGrow={1} wrapMode="none" style={titleStyle()}>
          <span style={titleStyle()}>{LIAISON_GLYPH}{" "}{title()}</span>
        </text>
        <Show when={props.workers() > 0}>
          <text flexShrink={0} wrapMode="none" style={tier("dim", P())}>{" "}{LIAISON_WORKERS_MARK}{props.workers()}</text>
        </Show>
      </box>
      {/* One dynamic child per box keeps the rows in source order. */}
      <box flexDirection="column">
        <For each={shown()}>
          {(item, i) => {
            const color = () => priorityColor(LIAISON_ITEM_PRIORITY[item.kind], P());
            const more = () => (i() === shown().length - 1 && hidden() > 0 ? ` +${hidden()}` : "");
            return (
              <box flexDirection="row">
                <text flexShrink={0} wrapMode="none" style={{ fg: color() }}>{LIAISON_ITEM_GLYPHS[LIAISON_ITEM_PRIORITY[item.kind]]}{" "}</text>
                <text flexGrow={1} wrapMode="none" style={tier("secondary", P())}>{item.title}</text>
                <text flexShrink={0} wrapMode="none" style={tier("muted", P())}>{" "}{formatAge(item.since, props.nowMs())}{more()}</text>
              </box>
            );
          }}
        </For>
      </box>
    </box>
    <BlockRule palette={P} />
    </box>
  );
}

/** Closing rule that sets a top block apart from what follows. */
export function BlockRule(props: { palette: Accessor<ThemePalette> }) {
  return (
    <box height={1} flexShrink={0} paddingLeft={1} paddingRight={1}>
      <text wrapMode="none" style={{ fg: props.palette().surface2 }}>{"─".repeat(200)}</text>
    </box>
  );
}
