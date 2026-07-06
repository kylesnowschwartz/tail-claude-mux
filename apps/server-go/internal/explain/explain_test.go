// Port of packages/runtime/test/explain.test.ts, plus a JSON-shape test
// locking the null/key contract the TS endpoint serializes.
package explain

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tracker"
	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/wire"
)

const testNow = int64(1_000_000_000)

// baseEvent mirrors the TS suite's ev() factory defaults.
func baseEvent() wire.AgentEvent {
	return wire.AgentEvent{
		Agent:   "claude-code",
		Session: "proj",
		Status:  wire.StatusRunning,
		TS:      testNow,
	}
}

func tierByID(t *testing.T, report Report, id string) Tier {
	t.Helper()
	for _, tier := range report.Lifecycle.Tiers {
		if tier.ID == id {
			return tier
		}
	}
	t.Fatalf("tier %q not found", id)
	return Tier{}
}

func eligibleMS(t *testing.T, tier Tier) int64 {
	t.Helper()
	if tier.EligibleInMS == nil {
		t.Fatalf("tier %q: eligibleInMs is null, want a value", tier.ID)
	}
	return *tier.EligibleInMS
}

func intPtr(v int) *int { return &v }

// "buildExplain — running + alive" / "reports reconcile + alive-ceiling
// tiers, governed by reconcile"
func TestRunningAliveGovernedByReconcile(t *testing.T) {
	ev := baseEvent()
	ev.Liveness = wire.LivenessAlive
	ev.PID = 4242
	ev.TS = testNow - 10_000
	report := Build(ev, testNow, tracker.ProbeNoSignal)

	if report.Status != wire.StatusRunning {
		t.Errorf("status = %q, want running", report.Status)
	}
	if report.Liveness != wire.LivenessAlive {
		t.Errorf("liveness = %q, want alive", report.Liveness)
	}
	if report.PID == nil || *report.PID != 4242 {
		t.Errorf("pid = %v, want 4242", report.PID)
	}
	if report.AgeMS != 10_000 {
		t.Errorf("ageMs = %d, want 10000", report.AgeMS)
	}

	reconcile := tierByID(t, report, TierReconcile)
	if !reconcile.Applies {
		t.Error("reconcile.applies = false, want true")
	}
	if got := eligibleMS(t, reconcile); got != ReconcileStaleMS-10_000 {
		t.Errorf("reconcile.eligibleInMs = %d, want %d", got, ReconcileStaleMS-10_000)
	}
	ceiling := tierByID(t, report, TierAliveCeiling)
	if !ceiling.Applies {
		t.Error("alive-ceiling.applies = false, want true")
	}
	if got := eligibleMS(t, ceiling); got != tracker.AlivePruneCeilingMS-10_000 {
		t.Errorf("alive-ceiling.eligibleInMs = %d, want %d", got, tracker.AlivePruneCeilingMS-10_000)
	}
	// prune-stuck (exited-only) and terminal tiers do not apply
	if tierByID(t, report, TierPruneStuck).Applies {
		t.Error("prune-stuck.applies = true, want false")
	}
	if tierByID(t, report, TierPruneTerminal).Applies {
		t.Error("prune-terminal.applies = true, want false")
	}

	// reconcile fires soonest → governs
	if report.Lifecycle.Governing != TierReconcile {
		t.Errorf("governing = %q, want reconcile", report.Lifecycle.Governing)
	}
}

// "once past the reconcile window the probe is eligible and verdict carries
// through"
func TestPastReconcileWindowProbeEligible(t *testing.T) {
	ev := baseEvent()
	ev.Liveness = wire.LivenessAlive
	ev.PID = 7
	ev.TS = testNow - (ReconcileStaleMS + 5_000)
	report := Build(ev, testNow, tracker.ProbeWorking)

	if !report.Probe.Eligible {
		t.Error("probe.eligible = false, want true")
	}
	if report.Probe.Verdict == nil || *report.Probe.Verdict != "working" {
		t.Errorf("probe.verdict = %v, want working", report.Probe.Verdict)
	}
	if got := eligibleMS(t, tierByID(t, report, TierReconcile)); got != 0 {
		t.Errorf("reconcile.eligibleInMs = %d, want 0", got)
	}
	// alive-ceiling is still the long backstop and reconcile governs the
	// tie (reconcile == 0 too, but it is declared first so it wins)
	if report.Lifecycle.Governing != TierReconcile {
		t.Errorf("governing = %q, want reconcile", report.Lifecycle.Governing)
	}
}

// "running + alive without a pid is not reconciled"
func TestRunningAliveWithoutPid(t *testing.T) {
	ev := baseEvent()
	ev.Liveness = wire.LivenessAlive
	ev.TS = testNow - 10_000
	report := Build(ev, testNow, tracker.ProbeNoSignal)

	if tierByID(t, report, TierReconcile).Applies {
		t.Error("reconcile.applies = true, want false")
	}
	if report.Probe.Eligible {
		t.Error("probe.eligible = true, want false")
	}
	// alive-ceiling still governs (the only applicable tier)
	if report.Lifecycle.Governing != TierAliveCeiling {
		t.Errorf("governing = %q, want alive-ceiling", report.Lifecycle.Governing)
	}
}

// "buildExplain — running + exited" / "governed by prune-stuck with
// countdown to the stuck timeout"
func TestRunningExitedGovernedByPruneStuck(t *testing.T) {
	ev := baseEvent()
	ev.Liveness = wire.LivenessExited
	ev.PID = 9
	ev.TS = testNow - 20_000
	report := Build(ev, testNow, tracker.ProbeNoSignal)

	stuck := tierByID(t, report, TierPruneStuck)
	if !stuck.Applies {
		t.Error("prune-stuck.applies = false, want true")
	}
	if got := eligibleMS(t, stuck); got != StuckRunningTimeoutMS-20_000 {
		t.Errorf("prune-stuck.eligibleInMs = %d, want %d", got, StuckRunningTimeoutMS-20_000)
	}
	if tierByID(t, report, TierReconcile).Applies {
		t.Error("reconcile.applies = true, want false")
	}
	if tierByID(t, report, TierAliveCeiling).Applies {
		t.Error("alive-ceiling.applies = true, want false")
	}
	if report.Lifecycle.Governing != TierPruneStuck {
		t.Errorf("governing = %q, want prune-stuck", report.Lifecycle.Governing)
	}
}

// "buildExplain — terminal status" / "done + exited → pruned immediately"
func TestDoneExitedPrunedImmediately(t *testing.T) {
	ev := baseEvent()
	ev.Status = wire.StatusDone
	ev.Liveness = wire.LivenessExited
	ev.TS = testNow - 60_000
	report := Build(ev, testNow, tracker.ProbeNoSignal)

	terminal := tierByID(t, report, TierPruneTerminal)
	if !terminal.Applies {
		t.Error("prune-terminal.applies = false, want true")
	}
	if got := eligibleMS(t, terminal); got != 0 {
		t.Errorf("prune-terminal.eligibleInMs = %d, want 0", got)
	}
	if report.Lifecycle.Governing != TierPruneTerminal {
		t.Errorf("governing = %q, want prune-terminal", report.Lifecycle.Governing)
	}
}

// "done + alive → no prune until process exits (stable)"
func TestDoneAliveIsStable(t *testing.T) {
	ev := baseEvent()
	ev.Status = wire.StatusDone
	ev.Liveness = wire.LivenessAlive
	ev.TS = testNow - 60_000
	report := Build(ev, testNow, tracker.ProbeNoSignal)

	if tierByID(t, report, TierPruneTerminal).Applies {
		t.Error("prune-terminal.applies = true, want false")
	}
	if report.Lifecycle.Governing != GoverningStable {
		t.Errorf("governing = %q, want stable", report.Lifecycle.Governing)
	}
}

// "buildExplain — idle/waiting" / "idle + exited → pruned immediately
// (eligibleInMs 0)"
func TestIdleExitedPrunedImmediately(t *testing.T) {
	ev := baseEvent()
	ev.Status = wire.StatusIdle
	ev.Liveness = wire.LivenessExited
	ev.TS = testNow - 1_000
	report := Build(ev, testNow, tracker.ProbeNoSignal)

	idle := tierByID(t, report, TierPruneIdle)
	if !idle.Applies {
		t.Error("prune-idle.applies = false, want true")
	}
	if got := eligibleMS(t, idle); got != 0 {
		t.Errorf("prune-idle.eligibleInMs = %d, want 0", got)
	}
	if report.Lifecycle.Governing != TierPruneIdle {
		t.Errorf("governing = %q, want prune-idle", report.Lifecycle.Governing)
	}
}

// "idle + alive → stable (pane scanner governs, no prune tier)"
func TestIdleAliveIsStable(t *testing.T) {
	ev := baseEvent()
	ev.Status = wire.StatusIdle
	ev.Liveness = wire.LivenessAlive
	ev.TS = testNow - 1_000
	report := Build(ev, testNow, tracker.ProbeNoSignal)

	if report.Lifecycle.Governing != GoverningStable {
		t.Errorf("governing = %q, want stable", report.Lifecycle.Governing)
	}
}

// "buildExplain — field projection" / "nulls out absent optional fields and
// reports present ones"
func TestFieldProjectionPresentFields(t *testing.T) {
	ev := baseEvent()
	ev.ThreadID = "thread-1"
	ev.PaneID = "%5"
	ev.WindowIndex = intPtr(2)
	ev.PaneIndex = intPtr(1)
	ev.ToolDescription = "Reading config.ts"
	ev.Liveness = wire.LivenessAlive
	ev.PID = 11
	report := Build(ev, testNow, tracker.ProbeNoSignal)

	if report.ThreadID == nil || *report.ThreadID != "thread-1" {
		t.Errorf("threadId = %v, want thread-1", report.ThreadID)
	}
	if report.PaneID == nil || *report.PaneID != "%5" {
		t.Errorf("paneId = %v, want %%5", report.PaneID)
	}
	if report.WindowIndex == nil || *report.WindowIndex != 2 {
		t.Errorf("windowIndex = %v, want 2", report.WindowIndex)
	}
	if report.PaneIndex == nil || *report.PaneIndex != 1 {
		t.Errorf("paneIndex = %v, want 1", report.PaneIndex)
	}
	if report.ToolDescription == nil || *report.ToolDescription != "Reading config.ts" {
		t.Errorf("toolDescription = %v, want Reading config.ts", report.ToolDescription)
	}
	if report.AgeMS != 0 {
		t.Errorf("ageMs = %d, want 0", report.AgeMS)
	}
}

// "unknown liveness when none recorded"
func TestUnknownLivenessWhenNoneRecorded(t *testing.T) {
	report := Build(baseEvent(), testNow, tracker.ProbeNoSignal)

	if report.Liveness != wire.LivenessUnknown {
		t.Errorf("liveness = %q, want unknown", report.Liveness)
	}
	if report.PID != nil {
		t.Errorf("pid = %v, want null", report.PID)
	}
	if report.ThreadID != nil {
		t.Errorf("threadId = %v, want null", report.ThreadID)
	}
}

// Go-only additions below: lock the serialized contract the TS endpoint
// emits, so the route wiring can trust the marshaled bytes.

func TestVerdictStringMapping(t *testing.T) {
	cases := []struct {
		name    string
		verdict tracker.ProbeVerdict
		want    string // "" = null
	}{
		{"no-signal is null", tracker.ProbeNoSignal, ""},
		{"working", tracker.ProbeWorking, "working"},
		{"ended", tracker.ProbeEnded, "ended"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Build(baseEvent(), testNow, tc.verdict).Probe.Verdict
			if tc.want == "" {
				if got != nil {
					t.Errorf("verdict = %q, want null", *got)
				}
			} else if got == nil || *got != tc.want {
				t.Errorf("verdict = %v, want %q", got, tc.want)
			}
		})
	}
}

// Stale entries never report a negative countdown (TS eligibleIn clamps at 0).
func TestEligibleInClampsAtZero(t *testing.T) {
	ev := baseEvent()
	ev.Liveness = wire.LivenessExited
	ev.TS = testNow - (StuckRunningTimeoutMS + 90_000)
	report := Build(ev, testNow, tracker.ProbeNoSignal)
	if got := eligibleMS(t, tierByID(t, report, TierPruneStuck)); got != 0 {
		t.Errorf("prune-stuck.eligibleInMs = %d, want 0 (clamped)", got)
	}
}

// The stable detail line omits "+liveness" when liveness is unknown and
// includes it otherwise (explain.ts detail template).
func TestStableDetailString(t *testing.T) {
	ev := baseEvent()
	ev.Status = wire.StatusDone // done + unknown liveness → stable
	report := Build(ev, testNow, tracker.ProbeNoSignal)
	if want := "done: no prune tier currently governs this entry"; report.Lifecycle.Detail != want {
		t.Errorf("detail = %q, want %q", report.Lifecycle.Detail, want)
	}

	ev.Liveness = wire.LivenessAlive // done + alive → stable, with suffix
	report = Build(ev, testNow, tracker.ProbeNoSignal)
	if want := "done+alive: no prune tier currently governs this entry"; report.Lifecycle.Detail != want {
		t.Errorf("detail = %q, want %q", report.Lifecycle.Detail, want)
	}
}

// Absent optional fields must serialize as explicit JSON nulls with the TS
// key names, and tiers must appear in declaration order.
func TestJSONShapeMatchesTSContract(t *testing.T) {
	report := Build(baseEvent(), testNow, tracker.ProbeNoSignal)
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	for _, want := range []string{
		`"threadId":null`, `"pid":null`, `"paneId":null`,
		`"windowIndex":null`, `"paneIndex":null`, `"toolDescription":null`,
		`"verdict":null`, `"eligibleInMs":null`,
		`"liveness":"unknown"`, `"governing":"stable"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("marshaled report missing %s in %s", want, s)
		}
	}
	// Tier order mirrors the TS tiers array (ties in the governing scan
	// resolve by this order, so it is contract, not cosmetics).
	order := []string{TierReconcile, TierPruneStuck, TierAliveCeiling, TierPruneTerminal, TierPruneIdle}
	last := -1
	for _, id := range order {
		idx := strings.Index(s, `"id":"`+id+`"`)
		if idx < 0 {
			t.Fatalf("tier %q missing from JSON", id)
		}
		if idx < last {
			t.Errorf("tier %q out of declaration order", id)
		}
		last = idx
	}
}
