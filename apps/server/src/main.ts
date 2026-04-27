import {
  ClaudeCodeHookAdapter,
  PiHookAdapter,
  startServer,
} from "@tcm/runtime";
import { createTmux } from "@tcm/mux-tmux";

const mux = createTmux();
const watchers = [new ClaudeCodeHookAdapter(), new PiHookAdapter()];

console.log(`Primary mux provider: ${mux.name}`);
console.log(`Agent watchers: ${watchers.map((w) => w.name).join(", ")}`);

startServer(mux, [], watchers);
