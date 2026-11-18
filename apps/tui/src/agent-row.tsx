/**
 * One agent row as the session list draws it: the dismiss slot, the agent
 * glyph and conversation name, and the status icon at the right edge.
 * Session cards and the compact list both draw their agents with it.
 */

import { createSignal, Show, type Accessor } from "solid-js";
import { TextAttributes } from "@opentui/core";
import { lerpHex, type SessionData, type Theme } from "@tcm/runtime";
import { AGENT_GLYPHS, SEV_ERROR, SEV_READY, SEV_STOPPED, SEV_WAITING, SEV_WORKING_SPINNER } from "./vocab";
import { tier, type TierStyle } from "./tiers";
import { isLiveAgent, statusLabel, type StatusLabel } from "./attention";
import { LiaisonWorkerMarks, statusColor } from "./liaison-block";

const SPINNERS = SEV_WORKING_SPINNER;
const BOLD = TextAttributes.BOLD;

/** Short display form for a threadId.
 *  Uses the last 4 chars of the ID because multiple agents (Codex, OpenCode)
 *  produce IDs with deterministic *prefixes* (UUIDv7 timestamp, `ses_`
 *  sigil) while their random bits live at the tail. For Claude Code's
 *  UUIDv4 the distribution is uniform, so the tail is just as good as the
 *  head. */
export function shortThreadId(id: string): string {
  return id.length <= 4 ? id : id.slice(-4);
}

export type SessionAgent = SessionData["agents"][number];

/** Live-agent count as a session header shows it: bare number capped at "9+", empty when none. */
export function liveAgentBadge(session: SessionData): string {
  const n = session.agents?.filter(isLiveAgent).length ?? 0;
  if (n === 0) return "";
  if (n >= 10) return "9+";
  return String(n);
}

export function liveFirstAgents(agents: readonly SessionAgent[]): SessionAgent[] {
  const live: SessionAgent[] = [];
  const inactive: SessionAgent[] = [];
  for (const agent of agents) (isLiveAgent(agent) ? live : inactive).push(agent);
  return live.concat(inactive);
}

export interface AgentListItemProps {
  agent: SessionData["agents"][number];
  palette: Accessor<Theme["palette"]>;
  spinIdx: Accessor<number>;
  // 0..1 sine-driven glow phase. Only consulted when this row is waiting;
  // upstream ticks the signal only while any row is waiting, so the closure
  // is dormant otherwise.
  glowT: Accessor<number>;
  isSessionSelected: boolean;
  isKeyboardFocused: boolean;
  isPaneFocused: boolean;
  /** An open liaison started this conversation; the row shows the worker mark before its name. */
  isLiaisonWorker: boolean;
  /** The liaison that started this conversation reports it finished and still open; the row shows a grey mark after the worker mark. */
  isLiaisonFinished?: boolean;
  onDismiss: () => void;
  onFocusPane: () => void;
}

export function AgentListItem(props: AgentListItemProps) {
  const P = () => props.palette();
  const [isDismissHover, setIsDismissHover] = createSignal(false);
  const [isFlash, setIsFlash] = createSignal(false);

  // Resolve the five-label scheme from tracker status + liveness
  const label = (): StatusLabel => statusLabel(props.agent);

  const isUnseen = () => props.agent.unseen === true;

  const icon = () => {
    const l = label();
    if (l === "working") return SPINNERS[props.spinIdx() % SPINNERS.length]!;
    if (l === "waiting") return SEV_WAITING;
    if (l === "ready") return SEV_READY;
    if (l === "stopped") return SEV_STOPPED;
    if (l === "error") return SEV_ERROR;
    return SEV_READY;
  };

  const color = () => statusColor(label(), P());

  const triggerFlash = () => {
    setIsFlash(true);
    setTimeout(() => setIsFlash(false), 150);
  };

  const bgColor = () => {
    if (isFlash()) return P().surface1;
    if (props.isKeyboardFocused) return P().surface0;
    // Focused-pane rows get the same row-bg treatment: the sky fg tint
    // alone is too subtle on light/transparent themes.
    if (props.isPaneFocused) return P().surface0;
    return "transparent";
  };

  const nameStyle = () => {
    const base = props.isSessionSelected
      ? {
          fg: props.isKeyboardFocused ? P().text : P().subtext1,
          attributes: props.isKeyboardFocused ? BOLD : undefined,
        }
      : tier("dim", P());
    const attentionFg = isUnseen() ? P().teal : base.fg;
    return {
      ...base,
      fg: label() === "waiting" ? lerpHex(attentionFg, P().yellow, props.glowT()) : attentionFg,
    };
  };
  // Supporting detail steps below muted on unselected cards, preserving
  // the selected/unselected contrast the tier scale doesn't encode.
  const supportingStyle = () => props.isSessionSelected
    ? tier("muted", P())
    : { fg: P().surface2 };
  // Focused-pane marker: the row's whole identity (glyph, subagent, thread
  // name) tints sky. A colour survives the right-edge clip that used to eat
  // the trailing "•" on long thread names.
  const focusTinted = (base: TierStyle) => props.isPaneFocused
    ? { fg: P().sky }
    : base;

  return (
    <box flexDirection="column" flexShrink={0} onMouseDown={() => {
      triggerFlash();
      props.onFocusPane();
    }}>
      <box
        flexDirection="column"
        backgroundColor={bgColor()}
        paddingRight={1}
      >
        {/* Row 1: dismiss + agent name + threadId ... unseen badge + status icon */}
        <box flexDirection="row">
          <text
            flexShrink={0}
            style={supportingStyle()}
            onMouseDown={(event) => {
              event.preventDefault();
              event.stopPropagation();
              props.onDismiss();
            }}
            onMouseOver={() => setIsDismissHover(true)}
            onMouseOut={() => setIsDismissHover(false)}
          >
            <span style={isDismissHover() ? { fg: P().red } : supportingStyle()}>{(props.agent.windowIndex != null ? String(props.agent.windowIndex).padStart(2, " ") : " ·") + " "}</span>
          </text>
          {/* wrapMode="none" is load-bearing: without it opentui measures
              at the default word-wrap height and yoga grows the row,
              spilling past the card's border (which does not
              clip; see the overflow note on the card frame). No
              `truncate`: it renders a middle-ellipsis; plain end-clip is
              the intended look. */}
          <text flexGrow={1} wrapMode="none" style={nameStyle()}>
            <span style={focusTinted(nameStyle())}>{AGENT_GLYPHS[props.agent.agent] ?? props.agent.agent}</span>
            <Show when={props.agent.subagent}>
              <span style={focusTinted(tier(props.isSessionSelected ? "dim" : "muted", P()))}>{"  "}{props.agent.subagent}</span>
            </Show>
            {/* Session name from the agent-ouija registry when known;
                the 4-char uuid suffix is the fallback identity. */}
            <Show when={props.isLiaisonWorker}>
              <LiaisonWorkerMarks palette={P} finished={!!props.isLiaisonFinished} finishedStyle={focusTinted(tier("muted", P()))} />
            </Show>
            <Show when={props.agent.threadName || props.agent.threadId}>
              <span style={focusTinted(supportingStyle())}>{props.isLiaisonWorker ? " " : "  "}{props.agent.threadName || shortThreadId(props.agent.threadId!)}</span>
            </Show>
          </text>
          <text flexShrink={0} style={{ fg: color() }}>
            <span style={{ fg: color() }}>{" "}{icon()}</span>
          </text>
        </box>
      </box>
    </box>
  );
}
