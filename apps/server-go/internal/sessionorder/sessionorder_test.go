package sessionorder

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSyncApplyReorderHide(t *testing.T) {
	o := Load("")
	o.Sync([]string{"a", "b", "c"})
	if got := o.Apply([]string{"a", "b", "c"}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("initial apply = %v", got)
	}

	o.Reorder("c", -1)
	if got := o.Apply([]string{"a", "b", "c"}); !reflect.DeepEqual(got, []string{"a", "c", "b"}) {
		t.Errorf("after reorder = %v", got)
	}

	o.Hide("a")
	if got := o.Apply([]string{"a", "b", "c"}); !reflect.DeepEqual(got, []string{"c", "b"}) {
		t.Errorf("after hide = %v", got)
	}

	o.Show("a")
	if got := o.Apply([]string{"a", "b", "c"}); !reflect.DeepEqual(got, []string{"a", "c", "b"}) {
		t.Errorf("after show = %v", got)
	}

	// Stale names drop, new names append at the end.
	o.Sync([]string{"c", "b", "d"})
	if got := o.Apply([]string{"c", "b", "d"}); !reflect.DeepEqual(got, []string{"c", "b", "d"}) {
		t.Errorf("after resync = %v", got)
	}
}

// The on-disk file is shared with the bun server: both of its shapes (bare
// array, {order,hidden} object) must load, and saves must round-trip.
func TestPersistence_BunCompatibleShapes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-order.json")

	if err := os.WriteFile(path, []byte(`["b","a"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	o := Load(path)
	if got := o.Apply([]string{"a", "b"}); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Errorf("bare-array load: apply = %v", got)
	}

	if err := os.WriteFile(path, []byte(`{"order":["b","a"],"hidden":["a"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	o = Load(path)
	if got := o.Apply([]string{"a", "b"}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("object load with hidden: apply = %v", got)
	}

	// A save with hidden sessions writes the object shape; the bun server
	// must be able to read it back (same key names).
	o.Sync([]string{"a", "b"})
	o.Hide("b")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || raw[0] != '{' {
		t.Errorf("save with hidden must use object shape, got %s", raw)
	}

	// Corrupt file: start fresh, don't crash.
	if err := os.WriteFile(path, []byte(`{broken`), 0o644); err != nil {
		t.Fatal(err)
	}
	o = Load(path)
	if got := o.Apply([]string{"x"}); !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("corrupt load: apply = %v", got)
	}
}
