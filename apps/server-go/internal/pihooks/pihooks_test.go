package pihooks

import (
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) (agentDir, source string) {
	t.Helper()
	agentDir = filepath.Join(t.TempDir(), "agent")
	if err := os.Mkdir(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source = filepath.Join(t.TempDir(), "tcm-status.ts")
	if err := os.WriteFile(source, []byte("export default () => {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return agentDir, source
}

func TestInstallFreshThenRepeat(t *testing.T) {
	agentDir, source := setup(t)
	other := filepath.Join(agentDir, "extensions", "mine.ts")
	if res, err := Install(agentDir, source); err != nil || res != Installed {
		t.Fatalf("fresh: %v %v", res, err)
	}
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(agentDir, "extensions", FileName))
	if err != nil || link != source {
		t.Fatalf("link %q %v", link, err)
	}
	if res, err := Install(agentDir, source); err != nil || res != AlreadyInstalled {
		t.Fatalf("repeat: %v %v", res, err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("other extension touched: %v", err)
	}
}

func TestInstallReplacesOwnStaleFile(t *testing.T) {
	agentDir, source := setup(t)
	target := filepath.Join(agentDir, "extensions", FileName)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := Install(agentDir, source); err != nil || res != Installed {
		t.Fatalf("replace: %v %v", res, err)
	}
	if link, _ := os.Readlink(target); link != source {
		t.Fatalf("link %q", link)
	}
}

func TestInstallSkipsMissingAgentDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	_, source := setup(t)
	if res, err := Install(missing, source); err != nil || res != NoAgentDir {
		t.Fatalf("%v %v", res, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("agent dir created")
	}
}
