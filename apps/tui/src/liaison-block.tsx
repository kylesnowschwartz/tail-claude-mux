/**
 * The sidebar's liaison block: the rows pinned at the top for one open
 * liaison, plus the status colours and the closing rule it shares with the
 * rest of the sidebar.
 */

import { For, Show, type Accessor } from "solid-js";
import { TextAttributes } from "@opentui/core";
import type { ThemePalette } from "@tcm/runtime";
import {
  SEV_WORKING_SPINNER,
  SEV_WAITING,
  SEV_READY,
  SEV_STOPPED,
  SEV_ERROR,
  LIAISON_GLYPH,
  LIAISON_WORKER_GLYPH,
} from "./vocab";
import { tier } from "./tiers";
import {
  formatAge,
  fleetFits,
  formatFleet,
  formatWorkers,
  liaisonTitle,
  statusLabel,
  statusStart,
  type SessionAgentRef,
  type StatusLabel,
} from "./attention";

const BOLD = TextAttributes.BOLD;
const KEY_WIDTH = 7; // Label column of the block's rows.
const BLOCK_PAD = 2; // Left and right padding of the block.

export function statusColor(label: StatusLabel, p: ThemePalette): string {
  if (label === "working") return p.blue;
  if (label === "waiting") return p.yellow;
  if (label === "ready") return p.green;
  if (label === "error") return p.red;
  return p.surface2;
}

/** Static status glyphs for compact counts; working shows the spinner's first frame. */
const STATUS_GLYPHS: Record<StatusLabel, string> = {
  working: SEV_WORKING_SPINNER[0],
  waiting: SEV_WAITING,
  ready: SEV_READY,
  stopped: SEV_STOPPED,
  error: SEV_ERROR,
};

export interface LiaisonBlockProps {
  liaison: SessionAgentRef;
  palette: Accessor<ThemePalette>;
  nowMs: Accessor<number>;
  fleet: Accessor<{ label: StatusLabel; count: number }[]>;
  /** Count of conversations this liaison started; the work row shows when it is above zero. */
  workers: Accessor<number>;
  width: Accessor<number>;
  needs: Accessor<string>;
  needsCount: Accessor<number>;
  /** The fleet and needs rows appear once, on the first liaison block; the needs row only while something needs the engineer. */
  showFleet: boolean;
}

/** Heads-up rows for one liaison: who it is, the fleet, and what waits. */
export function LiaisonBlock(props: LiaisonBlockProps) {
  const P = () => props.palette();
  const label = () => statusLabel(props.liaison.agent);
  const statusText = () => props.liaison.session.metadata?.status?.text ?? "";
  const title = () => liaisonTitle(props.liaison.agent.threadName ?? "");
  const age = () => formatAge(statusStart(props.liaison.agent), props.nowMs());
  // Narrow sidebars keep the whole title and show only the age, in the status colour.
  const headerFits = () => 2 + title().length + 1 + label().length + 1 + age().length <= props.width() - BLOCK_PAD;
  const key = (k: string) => <span style={tier("muted", P())}>{k.padEnd(KEY_WIDTH)}</span>;
  // Status words when they fit the row, else each status glyph with its count.
  const fleetInWords = () => fleetFits(props.fleet(), props.width() - BLOCK_PAD - KEY_WIDTH);
  return (
    <box flexDirection="column" flexShrink={0}>
    <box flexDirection="column" flexShrink={0} paddingLeft={1} paddingRight={1}>
      <box flexDirection="row">
        <text flexGrow={1} wrapMode="none">
          <span style={{ fg: P().blue }}>{LIAISON_GLYPH}{" "}</span>
          <span style={{ fg: P().text, attributes: BOLD }}>{title()}</span>
        </text>
        <text flexShrink={0} wrapMode="none">
          <Show when={headerFits()} fallback={
            <span style={{ fg: statusColor(label(), P()) }}>{" "}{age()}</span>
          }>
            <span style={{ fg: statusColor(label(), P()) }}>{" "}{label()}</span>
            <span style={tier("muted", P())}>{" "}{age()}</span>
          </Show>
        </text>
      </box>
      {/* One dynamic child per box keeps the rows in source order. */}
      <box flexDirection="column">
      <Show when={props.showFleet}>
      <text wrapMode="none">
        {key("fleet")}
        <Show when={props.fleet().length > 0} fallback={<span style={tier("muted", P())}>none</span>}>
          <Show when={fleetInWords()} fallback={
            <For each={props.fleet()}>
              {({ label, count }, i) => (
                <span style={{ fg: statusColor(label, P()) }}>{i() > 0 ? " " : ""}{STATUS_GLYPHS[label]}{count}</span>
              )}
            </For>
          }>
            <span style={{ fg: P().subtext0 }}>{formatFleet(props.fleet())}</span>
          </Show>
        </Show>
      </text>
      </Show>
      </box>
      <box flexDirection="column">
      <Show when={props.showFleet && props.needsCount() > 0}>
        <text wrapMode="none">
          {key("needs")}
          <span style={{ fg: P().peach }}>{props.needs()}</span>
        </text>
      </Show>
      </box>
      <box flexDirection="column">
      <Show when={props.workers() > 0}>
        <text wrapMode="none">
          {key("work")}
          <span style={{ fg: P().blue }}>{LIAISON_WORKER_GLYPH}{" "}</span>
          <span style={{ fg: P().subtext0 }}>{formatWorkers(props.workers(), props.width() - BLOCK_PAD - KEY_WIDTH - 2)}</span>
        </text>
      </Show>
      </box>
      <box flexDirection="column">
      <Show when={statusText()}>
        <text wrapMode="none">
          {key("status")}
          <span style={{ fg: P().subtext1 }}>{statusText()}</span>
        </text>
      </Show>
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
