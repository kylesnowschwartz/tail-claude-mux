/**
 * The compact session list: each session is one header row (name, a rule
 * that fills the width, the live-agent count at the right edge) followed by
 * one row per agent. No border, no branch row.
 */

import { For, Show, type Accessor } from "solid-js";
import { TextAttributes } from "@opentui/core";
import { lerpHex, type SessionData, type ThemePalette } from "@tcm/runtime";
import { tier } from "./tiers";
import { isLiaisonFinished, isLiaisonWorker, type SessionAgentRef } from "./attention";
import { AgentListItem, liveAgentBadge, liveFirstAgents, type SessionAgent } from "./agent-row";

const BOLD = TextAttributes.BOLD;

export interface CompactSessionHeaderProps {
  session: SessionData;
  isSelected: boolean;
  isCurrent: boolean;
  glowT: Accessor<number>;
  palette: Accessor<ThemePalette>;
  onSelect: () => void;
}

/** One row: session name, a rule to the right edge, the live-agent count. */
export function CompactSessionHeader(props: CompactSessionHeaderProps) {
  const P = () => props.palette();
  const nameStyle = () => {
    const base = tier(props.isSelected ? "primary" : "dim", P());
    const attentionFg = props.session.unseen ? P().teal : base.fg;
    return {
      ...base,
      fg: props.session.agentState?.status === "waiting"
        ? lerpHex(attentionFg, P().yellow, props.glowT())
        : attentionFg,
      attributes: (base.attributes ?? 0) | (props.isCurrent ? BOLD : 0),
    };
  };
  // The selected session's rule takes the selected card's border colour.
  const ruleStyle = () => ({ fg: props.isSelected ? P().blue : P().surface2 });
  const badge = () => liveAgentBadge(props.session);
  return (
    <box flexDirection="row" flexShrink={0} paddingRight={1} onMouseDown={props.onSelect}>
      <text flexShrink={1} wrapMode="none" style={nameStyle()}>
        <span style={nameStyle()}>{props.session.name}</span>
      </text>
      <box flexGrow={1} flexShrink={1} flexBasis={0} height={1} paddingLeft={1} minWidth={0}>
        <text wrapMode="none" style={ruleStyle()}>{"─".repeat(200)}</text>
      </box>
      <Show when={badge()}>
        <text flexShrink={0} wrapMode="none" style={tier(props.isSelected ? "secondary" : "dim", P())}>{" "}{badge()}</text>
      </Show>
    </box>
  );
}

export interface CompactSessionProps {
  session: SessionData;
  isSelected: boolean;
  isCurrent: boolean;
  visibleAgentCount: number;
  spinIdx: Accessor<number>;
  glowT: Accessor<number>;
  palette: Accessor<ThemePalette>;
  onSelect: () => void;
  panelFocus: Accessor<"sessions" | "agents">;
  focusedAgentIdx: Accessor<number>;
  focusedPaneId: Accessor<string | null>;
  /** Open liaisons; a row that one of them started shows the worker mark. */
  liaisons: Accessor<SessionAgentRef[]>;
  onAgentDismiss: (agent: SessionAgent) => void;
  onAgentFocus: (agent: SessionAgent) => void;
}

/** A session in the compact list: its header row, then one row per visible agent. */
export function CompactSession(props: CompactSessionProps) {
  const P = () => props.palette();
  const agents = () => liveFirstAgents(props.session.agents ?? []);
  const visibleAgents = () => agents().slice(0, props.visibleAgentCount);
  const hiddenAgentCount = () => Math.max(0, agents().length - visibleAgents().length);
  return (
    <box id={`session-${props.session.name}`} flexDirection="column" flexShrink={0}>
      <CompactSessionHeader
        session={props.session}
        isSelected={props.isSelected}
        isCurrent={props.isCurrent}
        glowT={props.glowT}
        palette={P}
        onSelect={props.onSelect}
      />
      <box flexDirection="column">
        <For each={visibleAgents()}>
          {(agent, i) => (
            <AgentListItem
              agent={agent}
              palette={P}
              spinIdx={props.spinIdx}
              glowT={props.glowT}
              isSessionSelected={props.isSelected}
              isKeyboardFocused={props.isSelected && props.panelFocus() === "agents" && i() === props.focusedAgentIdx()}
              isPaneFocused={agent.paneId != null && agent.paneId === props.focusedPaneId()}
              isLiaisonWorker={isLiaisonWorker(agent, props.liaisons())}
              isLiaisonFinished={isLiaisonFinished(agent, props.liaisons())}
              onDismiss={() => props.onAgentDismiss(agent)}
              onFocusPane={() => props.onAgentFocus(agent)}
            />
          )}
        </For>
      </box>
      <Show when={hiddenAgentCount() > 0}>
        <text wrapMode="none" style={tier(props.isSelected ? "dim" : "muted", P())}>
          {"   +"}{hiddenAgentCount()} more
        </text>
      </Show>
    </box>
  );
}
