# server-go

The Go backend for tcm (work in progress). Scope and staged plan:
`.agent-history/SCOPING-go-backend.md` at the repo root.

## wire

Frozen client↔server JSON contract: a field-for-field port of
`packages/runtime/src/shared.ts`, `contracts/agent.ts`, and
`contracts/parse-hook-payload.ts`. The TypeScript side is the contract of
record while both servers exist; mirror changes here and keep
`wire/testdata/state-live.json` (a live `GET /state` capture) current —
the tests strict-decode it, so an unmirrored field fails loudly.

```sh
cd apps/server-go && go test ./...
```
