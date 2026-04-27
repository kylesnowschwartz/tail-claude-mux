#!/usr/bin/env bun
/**
 * CLI entry point for registering tcm hooks in Claude Code's settings.json.
 * Usage: bun run scripts/setup-hooks.ts
 */

import { join } from "path";
import { registerHooks } from "../packages/runtime/src/setup/register-hooks";

const tcmDir = join(import.meta.dir, "..");

const added = registerHooks(tcmDir);

if (added.length === 0) {
  console.log("All hooks already registered.");
} else {
  console.log(`Registered hooks for: ${added.join(", ")}`);
}
