import type { MetadataTone, SessionMetadata } from "../shared";

const MAX_LOGS = 50;
const MAX_MESSAGE_LENGTH = 500;

function truncate(s: string, max: number = MAX_MESSAGE_LENGTH): string {
  return s.length > max ? s.slice(0, max - 1) + "…" : s;
}

export class SessionMetadataStore {
  private store = new Map<string, SessionMetadata>();

  private getOrCreate(session: string): SessionMetadata {
    let meta = this.store.get(session);
    if (!meta) {
      meta = { status: null, progress: null, logs: [] };
      this.store.set(session, meta);
    }
    return meta;
  }

  get(session: string): SessionMetadata | null {
    const meta = this.store.get(session);
    if (!meta) return null;
    // Return null if everything is empty
    if (!meta.status && !meta.progress && meta.logs.length === 0) return null;
    return meta;
  }

  setStatus(session: string, status: { text: string; tone?: MetadataTone } | null): void {
    if (!status) {
      const meta = this.store.get(session);
      if (meta) meta.status = null;
      return;
    }
    const meta = this.getOrCreate(session);
    meta.status = { text: truncate(status.text, 100), tone: status.tone, ts: Date.now() };
  }

  setProgress(session: string, progress: { current?: number; total?: number; percent?: number; label?: string } | null): void {
    if (!progress) {
      const meta = this.store.get(session);
      if (meta) meta.progress = null;
      return;
    }
    const meta = this.getOrCreate(session);
    meta.progress = {
      current: progress.current,
      total: progress.total,
      percent: progress.percent,
      label: progress.label ? truncate(progress.label, 100) : undefined,
      ts: Date.now(),
    };
  }

  appendLog(session: string, entry: { message: string; tone?: MetadataTone; source?: string }): void {
    const meta = this.getOrCreate(session);
    meta.logs.push({
      message: truncate(entry.message),
      tone: entry.tone,
      source: entry.source ? truncate(entry.source, 50) : undefined,
      ts: Date.now(),
    });
    if (meta.logs.length > MAX_LOGS) {
      meta.logs = meta.logs.slice(meta.logs.length - MAX_LOGS);
    }
  }

  clearLogs(session: string): void {
    const meta = this.store.get(session);
    if (meta) meta.logs = [];
  }

  /** Remove metadata for sessions that no longer exist */
  pruneSessions(validNames: Set<string>): void {
    for (const name of this.store.keys()) {
      if (!validNames.has(name)) this.store.delete(name);
    }
  }
}
