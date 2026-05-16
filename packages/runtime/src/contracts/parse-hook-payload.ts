import type { HookPayload } from "./agent-watcher";

/**
 * Wire-event validator at the POST /hook ingress.
 *
 * Returns a typed `HookPayload` when the input matches the contract, `null`
 * otherwise. Drop-on-malformed semantics: the server keeps its "always
 * returns 200 — hook failures must never block the agent" promise, so a
 * rejected payload is silently dropped rather than 4xx'd. The validator's
 * job is to give every downstream consumer a payload whose shape they can
 * trust, not to gate the response code.
 *
 * Design choices:
 *
 *   - Required fields (`event`, `session_id`, `cwd`) must be non-empty
 *     strings within reasonable size bounds — any failure rejects the whole
 *     event.
 *
 *   - Optional fields are typechecked individually. A malformed optional
 *     field is dropped (returned as `undefined`) rather than rejecting the
 *     whole event, so a future Claude Code release that adds a new field
 *     can't break us; we just ignore what we don't recognize.
 *
 *   - Event-name allow-listing is intentionally NOT done here. The watchers
 *     decide what to do with unknown events (typically: ignore them via
 *     `HOOK_STATUS_MAP[event] ?? null`). Gatekeeping at the wire boundary
 *     would couple this file to every upstream agent's hook taxonomy.
 *
 *   - The returned object is constructed by explicit field copy, so unknown
 *     properties on the input are dropped — defense against future fields
 *     the schema doesn't yet describe leaking into the type-asserted
 *     downstream code.
 */

const MAX_EVENT_LEN = 128;
const MAX_SESSION_ID_LEN = 256;
const MAX_AGENT_LEN = 64;
const MAX_TOOL_NAME_LEN = 128;
const MAX_NOTIFICATION_TYPE_LEN = 64;
const MAX_SESSION_NAME_LEN = 256;
const MAX_STRING_LEN = 64 * 1024;
const MAX_TOOL_INPUT_KEYS = 256;
const MAX_PROCESS_SNAPSHOT_LEN = 256 * 1024;

// Sets are typed to the corresponding HookPayload union member. If the union
// in agent-watcher.ts widens (new stop_reason / shutdown_reason value) or
// narrows, TypeScript fails to match the literal-array element types against
// `NonNullable<HookPayload["..."]>` and forces these to be updated in lockstep.
type StopReason = NonNullable<HookPayload["stop_reason"]>;
type ShutdownReason = NonNullable<HookPayload["shutdown_reason"]>;
const VALID_STOP_REASONS: ReadonlySet<StopReason> = new Set<StopReason>([
  "stop",
  "length",
  "toolUse",
  "error",
  "aborted",
]);
const VALID_SHUTDOWN_REASONS: ReadonlySet<ShutdownReason> = new Set<ShutdownReason>([
  "quit",
  "reload",
  "new",
  "resume",
  "fork",
]);

export function parseHookPayload(input: unknown): HookPayload | null {
  if (!input || typeof input !== "object" || Array.isArray(input)) return null;
  const obj = input as Record<string, unknown>;

  const event = obj.event;
  const sessionId = obj.session_id;
  const cwd = obj.cwd;
  if (typeof event !== "string" || event.length === 0 || event.length > MAX_EVENT_LEN) return null;
  if (typeof sessionId !== "string" || sessionId.length === 0 || sessionId.length > MAX_SESSION_ID_LEN) return null;
  // Empty cwd is malformed: downstream `resolveSession(cwd)` would fail anyway,
  // and accepting it asymmetrically with `event`/`session_id` invited confusion.
  if (typeof cwd !== "string" || cwd.length === 0 || cwd.length > MAX_STRING_LEN) return null;

  return {
    event,
    session_id: sessionId,
    cwd,
    agent: optBoundedString(obj.agent, MAX_AGENT_LEN),
    tool_name: optBoundedString(obj.tool_name, MAX_TOOL_NAME_LEN),
    tool_input: optPlainObject(obj.tool_input, MAX_TOOL_INPUT_KEYS),
    notification_type: optBoundedString(obj.notification_type, MAX_NOTIFICATION_TYPE_LEN),
    session_name: optBoundedString(obj.session_name, MAX_SESSION_NAME_LEN),
    tool_is_error: typeof obj.tool_is_error === "boolean" ? obj.tool_is_error : undefined,
    stop_reason: optEnumString(obj.stop_reason, VALID_STOP_REASONS),
    error_message: optBoundedString(obj.error_message, MAX_STRING_LEN),
    shutdown_reason: optEnumString(obj.shutdown_reason, VALID_SHUTDOWN_REASONS),
    pid: optPositiveInteger(obj.pid),
    process_snapshot: optBoundedString(obj.process_snapshot, MAX_PROCESS_SNAPSHOT_LEN),
  };
}

/** A positive integer (pid > 1), or undefined. pids 0/1 are kernel/init and
 *  never represent an agent process — treat as malformed. */
function optPositiveInteger(value: unknown): number | undefined {
  if (typeof value !== "number" || !Number.isInteger(value) || value <= 1) return undefined;
  return value;
}

/** A bounded-length non-empty string, or undefined. */
function optBoundedString(value: unknown, maxLen: number): string | undefined {
  if (typeof value !== "string") return undefined;
  if (value.length === 0 || value.length > maxLen) return undefined;
  return value;
}

/** A plain object (not array, not null) with at most maxKeys own keys, or undefined. */
function optPlainObject(value: unknown, maxKeys: number): Record<string, unknown> | undefined {
  if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
  const keys = Object.keys(value as object);
  if (keys.length > maxKeys) return undefined;
  return value as Record<string, unknown>;
}

/** A string belonging to the given allow-set, or undefined. Generic so the
 *  return type is narrowed to the set's element type (no cast at call site). */
function optEnumString<T extends string>(value: unknown, allowed: ReadonlySet<T>): T | undefined {
  return typeof value === "string" && allowed.has(value as T) ? (value as T) : undefined;
}
