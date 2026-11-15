package tmux

import (
	"strings"
	"testing"

	"github.com/kylesnowschwartz/tail-claude-mux/apps/server-go/internal/tmux/tmuxtest"
)

const (
	testParentID = "11111111-2222-4333-8444-555555555555"
	testWorkerID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

// liaisonSpec is a parseable pane row with the given liaison option values.
func liaisonSpec(id, work, parent, worker string) tmuxtest.PaneSpec {
	return tmuxtest.PaneSpec{
		Session: "proj", ID: id, PID: "100", Dir: "/p", WindowActive: "1",
		WindowIndex: "0", PaneIndex: "0", WindowID: "@1",
		LiaisonWork: work, LiaisonParent: parent, LiaisonWorker: worker,
		Title: "title of " + id,
	}
}

func TestListAllPanes_LiaisonOptions(t *testing.T) {
	cases := []struct {
		name                 string
		work, parent, worker string
		wantWork, wantParent string
		wantWorker           string
	}{
		{name: "valid values", work: "w12", parent: testParentID, worker: testWorkerID,
			wantWork: "w12", wantParent: testParentID, wantWorker: testWorkerID},
		{name: "six digits", work: "w123456", parent: testParentID, worker: testWorkerID,
			wantWork: "w123456", wantParent: testParentID, wantWorker: testWorkerID},
		{name: "upper-case conversation IDs", work: "w1", parent: strings.ToUpper(testWorkerID), worker: strings.ToUpper(testWorkerID),
			wantWork: "w1"},
		{name: "no options"},
		{name: "tab in the work ID", work: "w1\t2", parent: testParentID, worker: testWorkerID,
			wantParent: testParentID, wantWorker: testWorkerID},
		{name: "line break in the parent", work: "w1", parent: testParentID[:18] + "\n" + testParentID[19:], worker: testWorkerID,
			wantWork: "w1", wantWorker: testWorkerID},
		{name: "line break at the end of the worker", work: "w1", parent: testParentID, worker: testWorkerID + "\n",
			wantWork: "w1", wantParent: testParentID},
		{name: "a forged row inside a value", work: "w1\t" + testParentID + "\t" + testWorkerID + "\tforged title\nproj\t%9\t1",
			parent: testParentID, worker: testWorkerID,
			wantParent: testParentID, wantWorker: testWorkerID},
		{name: "work ID of the wrong form", work: "w1234567", parent: testParentID, worker: testWorkerID,
			wantParent: testParentID, wantWorker: testWorkerID},
		{name: "work ID with no digits", work: "w", parent: testParentID, worker: testWorkerID,
			wantParent: testParentID, wantWorker: testWorkerID},
		{name: "work ID with a space", work: " w1", parent: testParentID, worker: testWorkerID,
			wantParent: testParentID, wantWorker: testWorkerID},
		{name: "conversation IDs of the wrong form", work: "w1", parent: "not-a-uuid", worker: testWorkerID + "0",
			wantWork: "w1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A plain row on each side shows the odd value moves no
			// neighbour and loses none.
			tm := &Tmux{Run: fake(map[string]string{
				"list-panes": tmuxtest.Listing(
					liaisonSpec("%1", "", "", "").Row(),
					liaisonSpec("%2", tc.work, tc.parent, tc.worker).Row(),
					liaisonSpec("%3", "w3", testParentID, testWorkerID).Row(),
				),
			})}
			got := tm.ListAllPanes()
			if len(got) != 3 {
				t.Fatalf("got %d panes, want 3: %+v", len(got), got)
			}
			p := got[1]
			if p.ID != "%2" || p.Title != "title of %2" || p.PID != 100 || p.Dir != "/p" {
				t.Errorf("pane = %+v, want %%2 with its own title, pid and dir", p)
			}
			if p.LiaisonWorkID != tc.wantWork || p.LiaisonParentThreadID != tc.wantParent || p.LiaisonWorkerThreadID != tc.wantWorker {
				t.Errorf("link = %q %q %q, want %q %q %q", p.LiaisonWorkID, p.LiaisonParentThreadID, p.LiaisonWorkerThreadID,
					tc.wantWork, tc.wantParent, tc.wantWorker)
			}
			if first := got[0]; first.ID != "%1" || first.LiaisonWorkID != "" || first.Title != "title of %1" {
				t.Errorf("row before = %+v", first)
			}
			if last := got[2]; last.ID != "%3" || last.LiaisonWorkID != "w3" || last.Title != "title of %3" {
				t.Errorf("row after = %+v", last)
			}
		})
	}
}

// A title keeps its tabs when the liaison values come before it.
func TestListAllPanes_LiaisonOptionsKeepTitleTabs(t *testing.T) {
	spec := liaisonSpec("%1", "w1", testParentID, testWorkerID)
	spec.Title = "title\twith\ttabs"
	tm := &Tmux{Run: fake(map[string]string{"list-panes": spec.Row()})}
	got := tm.ListAllPanes()
	if len(got) != 1 || got[0].Title != "title\twith\ttabs" || got[0].LiaisonWorkID != "w1" {
		t.Errorf("got %+v", got)
	}
}

// A row whose stated lengths do not fit its values is dropped, and the rows
// after it still parse.
func TestListAllPanes_LiaisonLengthMismatchDropsOnlyThatRow(t *testing.T) {
	bad := strings.Replace(liaisonSpec("%1", "w1", "", "").Row(), "\t2\t0\t0\tw1\t", "\t9\t0\t0\tw1\t", 1)
	tm := &Tmux{Run: fake(map[string]string{
		"list-panes": tmuxtest.Listing(bad, liaisonSpec("%2", "w2", testParentID, testWorkerID).Row()),
	})}
	got := tm.ListAllPanes()
	if len(got) != 1 || got[0].ID != "%2" || got[0].LiaisonWorkID != "w2" {
		t.Errorf("got %+v, want only %%2", got)
	}
}

// The format asks for each liaison option's length ahead of its value, and
// for the title last.
func TestListAllPanes_FormatReadsLiaisonOptionsByLength(t *testing.T) {
	var format string
	tm := &Tmux{Run: func(args ...string) (string, error) {
		format = args[len(args)-1]
		return "", nil
	}}
	tm.ListAllPanes()
	want := "#{n:@liaison-work}\t#{n:@liaison-parent}\t#{n:@liaison-worker}\t" +
		"#{@liaison-work}\t#{@liaison-parent}\t#{@liaison-worker}\t#{pane_title}"
	if !strings.HasSuffix(format, want) {
		t.Errorf("format = %q, want suffix %q", format, want)
	}
	if n := strings.Count(format, "\t") + 1; n != paneHeadFields+4 {
		t.Errorf("format has %d fields, want %d", n, paneHeadFields+4)
	}
}
