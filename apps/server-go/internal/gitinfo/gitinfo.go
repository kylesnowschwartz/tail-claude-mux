// Package gitinfo answers the sidebar's three git questions per session
// directory: branch, dirty, and worktree-ness. Port of the getGitInfo
// helper in packages/runtime/src/server/index.ts, including its 5s TTL
// cache (git status on large repos is the expensive call in the state
// loop). The shell pipeline is replaced by two direct git execs — same
// answers, no sh -c string interpolation.
package gitinfo

import (
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Info mirrors the TS GitInfo shape feeding SessionData.
type Info struct {
	Branch     string
	Dirty      bool
	IsWorktree bool
}

const cacheTTL = 5 * time.Second

type cached struct {
	info Info
	at   time.Time
}

// Cache is a TTL cache of per-directory git probes. Safe for concurrent use.
type Cache struct {
	mu      sync.Mutex
	entries map[string]cached
	now     func() time.Time
	probe   func(dir string) Info
}

// NewCache returns a Cache probing real git.
func NewCache() *Cache {
	return &Cache{entries: map[string]cached{}, now: time.Now, probe: probeGit}
}

// Get returns the git info for dir, probing at most once per TTL.
func (c *Cache) Get(dir string) Info {
	if dir == "" {
		return Info{}
	}
	c.mu.Lock()
	if e, ok := c.entries[dir]; ok && c.now().Sub(e.at) < cacheTTL {
		c.mu.Unlock()
		return e.info
	}
	c.mu.Unlock()

	info := c.probe(dir)

	c.mu.Lock()
	c.entries[dir] = cached{info: info, at: c.now()}
	c.mu.Unlock()
	return info
}

// Invalidate drops one directory's cache entry (or all with "").
func (c *Cache) Invalidate(dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if dir == "" {
		c.entries = map[string]cached{}
		return
	}
	delete(c.entries, dir)
}

// probeGit runs the two git commands the TS pipeline combined:
// rev-parse for branch + git-dir (one exec, two output lines), then
// status --porcelain for dirtiness. A non-repo directory returns the
// zero Info, matching the TS empty-output path.
func probeGit(dir string) Info {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD", "--git-dir").Output()
	if err != nil {
		return Info{}
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	info := Info{Branch: lines[0]}
	if len(lines) > 1 {
		info.IsWorktree = strings.Contains(lines[1], "/worktrees/")
	}
	status, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err == nil {
		info.Dirty = len(strings.TrimSpace(string(status))) > 0
	}
	return info
}
