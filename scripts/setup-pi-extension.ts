#!/usr/bin/env bun
/**
 * Install the opensessions pi extension by symlinking
 * `integrations/pi-extension/` into `~/.pi/agent/extensions/tcm`.
 * Idempotent: safe to rerun.
 */

import { existsSync, mkdirSync, lstatSync, readlinkSync, unlinkSync, symlinkSync } from "fs";
import { homedir } from "os";
import { join, resolve } from "path";

const repoRoot = resolve(import.meta.dir, "..");
const source = join(repoRoot, "integrations", "pi-extension");
const extensionsDir = join(homedir(), ".pi", "agent", "extensions");
const target = join(extensionsDir, "opensessions");

if (!existsSync(source)) {
  console.error(`Source directory missing: ${source}`);
  process.exit(1);
}

mkdirSync(extensionsDir, { recursive: true });

// Check if target already exists and points where we want.
if (existsSync(target) || isDanglingSymlink(target)) {
  const stat = lstatSync(target);
  if (stat.isSymbolicLink()) {
    const current = readlinkSync(target);
    if (current === source) {
      console.log(`Already linked: ${target} -> ${source}`);
      process.exit(0);
    }
    console.log(`Replacing stale symlink: ${target} -> ${current}`);
    unlinkSync(target);
  } else {
    console.error(
      `Refusing to clobber non-symlink at ${target}.\n` +
      `Remove it manually and rerun.`,
    );
    process.exit(1);
  }
}

symlinkSync(source, target);
console.log(`Linked: ${target} -> ${source}`);
console.log(
  `\nNext step: start a new pi session inside a tmux/zellij session that\n` +
  `opensessions is monitoring. Pi will appear in the sidebar HUD.`,
);

function isDanglingSymlink(path: string): boolean {
  try {
    lstatSync(path);
    return true;
  } catch {
    return false;
  }
}
