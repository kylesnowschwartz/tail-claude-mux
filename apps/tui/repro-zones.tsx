// Minimal repro: do click zones inside a bordered fixed-height
// overflow-hidden box align with their rendered rows? Mirrors the focused
// session card structure (index.tsx:1058). Run: bun run repro-zones.tsx
import { testRender } from "@opentui/solid";

const clicks: string[] = [];

function Row(p: { label: string }) {
  return (
    <box flexDirection="column" flexShrink={0} onMouseDown={() => clicks.push(p.label)}>
      <box flexDirection="row">
        <text>{p.label}</text>
      </box>
    </box>
  );
}

function App() {
  // Mirror the rolodex: flexGrow spacer above, chevron rule, bordered card,
  // chevron rule, flexGrow spacer below (index.tsx App layout).
  return (
    <box flexDirection="column" flexGrow={1}>
      <box flexGrow={1} />
      <text>{"── ^ ──"}</text>
      <box border borderStyle="rounded" flexShrink={0} height={6} overflow="hidden">
        <box flexDirection="column">
          <box flexDirection="row" paddingLeft={1} onMouseDown={() => clicks.push("name")}>
            <text>session-name</text>
          </box>
          <box flexDirection="column" paddingLeft={1}>
            <Row label="row1" />
            <Row label="row2" />
            <Row label="row3" />
          </box>
        </box>
      </box>
      <text>{"── v ──"}</text>
      <box flexGrow={1} />
    </box>
  );
}

const { renderOnce, captureCharFrame, mockMouse, mockInput } = await testRender(() => <App />, {
  width: 30,
  height: 14,
});

await renderOnce();
const frame = captureCharFrame();
const lines = frame.split("\n");
console.log("--- rendered frame ---");
lines.forEach((l, i) => console.log(i, JSON.stringify(l.slice(0, 24))));

// Click column 5 on every rendered line; report which handler fires.
for (let y = 0; y < 12; y++) {
  clicks.length = 0;
  await mockMouse.click(5, y);
  await renderOnce();
  console.log(`click y=${y} -> ${clicks.length ? clicks.join(",") : "(nothing)"}`);
}

// Same clicks through the REAL stdin path: raw SGR sequences (1-based
// coords, as tmux forwards them). Render row R = SGR y R+1.
console.log("--- raw SGR path ---");
for (const [label, sgrY] of [["name", 6], ["row1", 7], ["row2", 8], ["row3", 9]] as const) {
  clicks.length = 0;
  mockInput.pressKey(`\x1b[<0;6;${sgrY}M`);
  mockInput.pressKey(`\x1b[<0;6;${sgrY}m`);
  await renderOnce();
  console.log(`SGR y=${sgrY} (${label} row) -> ${clicks.length ? clicks.join(",") : "(nothing)"}`);
}
process.exit(0);
