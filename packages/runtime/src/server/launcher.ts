import { existsSync } from "fs";
import { join } from "path";
import { connect } from "net";
import { SERVER_PORT, SERVER_HOST } from "../shared";

async function isPortOpen(host: string, port: number, timeoutMs = 200): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = connect({ host, port });
    const timer = setTimeout(() => { socket.destroy(); resolve(false); }, timeoutMs);
    socket.on("connect", () => { clearTimeout(timer); socket.destroy(); resolve(true); });
    socket.on("error", () => { clearTimeout(timer); resolve(false); });
  });
}

/** Resolve the Go server binary (apps/server-go, built to bin/tcm-server by
 *  restart.sh). The bun server is retired — there is no fallback; a missing
 *  binary is a build error to surface, not to paper over. */
function resolveServerBin(): string {
  const candidates = [
    process.env.TCM_DIR
      ? join(process.env.TCM_DIR, "apps", "server-go", "bin", "tcm-server")
      : "",
    new URL("../../../../apps/server-go/bin/tcm-server", import.meta.url).pathname,
  ];
  for (const bin of candidates) {
    if (bin && existsSync(bin)) return bin;
  }
  throw new Error(
    "tcm-server binary not found — build it with: cd apps/server-go && go build -o bin/tcm-server ./cmd/tcm-server (or run scripts/restart.sh)",
  );
}

export async function ensureServer(): Promise<void> {
  // A live server on the port wins — the pid file is for stop.sh, not the
  // spawn gate (a hand-started A/B server may not have written one).
  if (await isPortOpen(SERVER_HOST, SERVER_PORT)) return;

  const proc = Bun.spawn([resolveServerBin()], {
    stdio: ["ignore", "ignore", "ignore"],
    env: { ...process.env },
  });
  proc.unref();

  for (let i = 0; i < 60; i++) {
    await Bun.sleep(50);
    if (await isPortOpen(SERVER_HOST, SERVER_PORT, 100)) return;
  }

  throw new Error("Server failed to start within 3 seconds");
}
