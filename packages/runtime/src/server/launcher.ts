import { existsSync, readFileSync } from "fs";
import { join } from "path";
import { connect } from "net";
import { SERVER_PORT, SERVER_HOST, PID_FILE } from "../shared";

function isProcessAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

async function isPortOpen(host: string, port: number, timeoutMs = 200): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = connect({ host, port });
    const timer = setTimeout(() => { socket.destroy(); resolve(false); }, timeoutMs);
    socket.on("connect", () => { clearTimeout(timer); socket.destroy(); resolve(true); });
    socket.on("error", () => { clearTimeout(timer); resolve(false); });
  });
}

function resolveServerEntryPath(): string {
  const envDir = process.env.TCM_DIR;
  if (envDir) {
    const fromEnv = join(envDir, "apps", "server", "src", "main.ts");
    if (existsSync(fromEnv)) return fromEnv;
  }

  const fromWorkspace = new URL("../../../../apps/server/src/main.ts", import.meta.url).pathname;
  if (existsSync(fromWorkspace)) return fromWorkspace;

  return fromWorkspace;
}

export async function ensureServer(): Promise<void> {
  if (existsSync(PID_FILE)) {
    const pid = parseInt(readFileSync(PID_FILE, "utf-8").trim(), 10);
    if (!isNaN(pid) && isProcessAlive(pid) && await isPortOpen(SERVER_HOST, SERVER_PORT)) {
      return;
    }
  }

  const serverPath = resolveServerEntryPath();
  const proc = Bun.spawn([process.execPath, "run", serverPath], {
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
