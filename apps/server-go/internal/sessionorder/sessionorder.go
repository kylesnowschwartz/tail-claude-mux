// Package sessionorder ports packages/runtime/src/server/session-order.ts:
// custom sidebar ordering plus hidden sessions, persisted as JSON. The
// on-disk format is shared with the bun server — a bare array (no hidden
// sessions) or {"order": [...], "hidden": [...]}.
package sessionorder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// Order maintains the custom order and hidden set. Not safe for concurrent
// use; the server serializes access through its command loop.
type Order struct {
	order       []string
	hidden      map[string]bool
	persistPath string // empty = in-memory only
}

type persisted struct {
	Order  []string `json:"order"`
	Hidden []string `json:"hidden,omitempty"`
}

// Load reads the persisted order from path (empty path or corrupt/missing
// file starts fresh — same tolerance as the TS constructor).
func Load(path string) *Order {
	o := &Order{hidden: map[string]bool{}, persistPath: path}
	if path == "" {
		return o
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return o
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		o.order = arr
		return o
	}
	var p persisted
	if json.Unmarshal(raw, &p) == nil {
		o.order = p.Order
		for _, n := range p.Hidden {
			o.hidden[n] = true
		}
	}
	return o
}

// Sync reconciles with the live session list: stale names drop, new names
// append at the end.
func (o *Order) Sync(names []string) {
	live := map[string]bool{}
	for _, n := range names {
		live[n] = true
	}
	o.order = slices.DeleteFunc(o.order, func(n string) bool { return !live[n] })
	for n := range o.hidden {
		if !live[n] {
			delete(o.hidden, n)
		}
	}
	for _, n := range names {
		if !slices.Contains(o.order, n) {
			o.order = append(o.order, n)
		}
	}
}

// Apply returns names minus hidden sessions, sorted by the custom order
// (unknown names sort last, stable).
func (o *Order) Apply(names []string) []string {
	pos := map[string]int{}
	for i, n := range o.order {
		pos[n] = i
	}
	visible := make([]string, 0, len(names))
	for _, n := range names {
		if !o.hidden[n] {
			visible = append(visible, n)
		}
	}
	sort.SliceStable(visible, func(i, j int) bool {
		return rank(pos, visible[i]) < rank(pos, visible[j])
	})
	return visible
}

// Reorder swaps name with its neighbor (delta -1 = up, 1 = down).
func (o *Order) Reorder(name string, delta int) {
	idx := slices.Index(o.order, name)
	if idx < 0 {
		return
	}
	to := idx + delta
	if to < 0 || to >= len(o.order) {
		return
	}
	o.order[idx], o.order[to] = o.order[to], o.order[idx]
	o.save()
}

// Hide removes a session from the panel without touching tmux.
func (o *Order) Hide(name string) {
	if !slices.Contains(o.order, name) || o.hidden[name] {
		return
	}
	o.hidden[name] = true
	o.save()
}

// Show makes a hidden session visible again.
func (o *Order) Show(name string) {
	if !o.hidden[name] {
		return
	}
	delete(o.hidden, name)
	if !slices.Contains(o.order, name) {
		o.order = append(o.order, name)
	}
	o.save()
}

// ShowAll clears the hidden set.
func (o *Order) ShowAll() {
	if len(o.hidden) == 0 {
		return
	}
	o.hidden = map[string]bool{}
	o.save()
}

// save persists best-effort in the bun-compatible shape.
func (o *Order) save() {
	if o.persistPath == "" {
		return
	}
	var doc any
	if len(o.hidden) == 0 {
		doc = o.order
	} else {
		hidden := make([]string, 0, len(o.hidden))
		for n := range o.hidden {
			hidden = append(hidden, n)
		}
		sort.Strings(hidden)
		doc = persisted{Order: o.order, Hidden: hidden}
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(o.persistPath), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(o.persistPath, append(data, '\n'), 0o644)
}

func rank(pos map[string]int, name string) int {
	if p, ok := pos[name]; ok {
		return p
	}
	return int(^uint(0) >> 1) // unknown names sort last
}
