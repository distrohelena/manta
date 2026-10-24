package manta

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestChangedMapReplaysMap checks ChangedMap against Map on a real replay:
// a shadow state built from Map at creation and then only from ChangedMap
// must equal Map after every update. Set MANTA_CHANGED_REPLAY to a .dem file
// (optionally MANTA_CHANGED_TICKS, default 20000, 0 = whole replay; and
// MANTA_CHANGED_EVERY, default 1, to compare with Map only on every Nth
// update: the shadow is still fed every update, so any divergence persists
// until a later comparison) to run it.
func TestChangedMapReplaysMap(t *testing.T) {
	path := os.Getenv("MANTA_CHANGED_REPLAY")
	if path == "" {
		t.Skip("MANTA_CHANGED_REPLAY not set")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p, err := NewStreamParser(f)
	if err != nil {
		t.Fatal(err)
	}
	ticks := uint32(20000)
	if s := os.Getenv("MANTA_CHANGED_TICKS"); s != "" {
		n, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		ticks = uint32(n)
	}
	if ticks > 0 {
		p.parseToTick(ticks)
	}
	every := 1
	if s := os.Getenv("MANTA_CHANGED_EVERY"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("bad MANTA_CHANGED_EVERY %q", s)
		}
		every = n
	}

	// MANTA_CHANGED_FAST=1 compares with an equivalent of Map that caches
	// field path names and builds no map (same paths, names and values), so
	// a check of every update of a whole replay fits in a few minutes.
	fast := os.Getenv("MANTA_CHANGED_FAST") == "1"
	names := fpNames{}

	shadow := map[*Entity]map[string]interface{}{}
	var updates, changedFields, checked, removed int
	p.OnEntity(func(e *Entity, op EntityOp) error {
		switch {
		case op.Flag(EntityOpCreated):
			for k := range e.ChangedMap() {
				if _, ok := e.Map()[k]; !ok {
					t.Fatalf("tick %d: create of %s: changed field %s not in Map", p.Tick, e, k)
				}
			}
			shadow[e] = e.Map()
		case op.Flag(EntityOpDeleted):
			if len(e.ChangedMap()) != 0 {
				t.Fatalf("tick %d: delete of %s has changed fields", p.Tick, e)
			}
			delete(shadow, e)
		case op.Flag(EntityOpUpdated):
			s := shadow[e]
			if s == nil {
				t.Fatalf("tick %d: update of unknown %s", p.Tick, e)
			}
			updates++
			// Shrunk arrays and tables drop their trailing elements
			// (heldota fork): remove them from the shadow first.
			for name, n := range e.ChangedLengths() {
				removed += dropElements(s, name, n)
			}
			for k, v := range e.ChangedMap() {
				s[k] = v
				changedFields++
			}
			if updates%every != 0 {
				return nil
			}
			if fast {
				if !names.matches(e, s) {
					t.Fatalf("tick %d: %s: state from ChangedMap differs from Map: %v", p.Tick, e, mapDiff(s, e.Map()))
				}
			} else if m := e.Map(); len(mapDiff(s, m)) != 0 {
				t.Fatalf("tick %d: %s: state from ChangedMap differs from Map: %v", p.Tick, e, mapDiff(s, m))
			}
			checked++
		}
		return nil
	})
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no updates checked")
	}
	t.Logf("%d updates checked, %d changed fields, %d elements removed by shrinks", checked, changedFields, removed)

	// Outside a handler the change set is released.
	for _, e := range p.entities {
		if e != nil && len(e.ChangedMap()) != 0 {
			t.Fatalf("%s: ChangedMap not empty outside a handler", e)
		}
		if e != nil && len(e.ChangedLengths()) != 0 {
			t.Fatalf("%s: ChangedLengths not empty outside a handler", e)
		}
	}
}

type fpKey struct {
	c  *class
	fp fieldPath
}

// fpNames caches class field path names for the fast comparison.
type fpNames map[fpKey]string

// seenNames is scratch for fpNames.matches (Map keeps one entry per name).
var seenNames = map[string]struct{}{}

// matches reports whether shadow equals e.Map(): every path Map lists has
// the same name and value in shadow, and shadow has no other key.
func (n fpNames) matches(e *Entity, shadow map[string]interface{}) bool {
	root := newFieldPath()
	fps := e.class.getFieldPaths(root, e.state)
	root.release()
	clear(seenNames)
	ok := true
	for _, fp := range fps {
		k := fpKey{e.class, *fp}
		name, hit := n[k]
		if !hit {
			name = e.class.getNameForFieldPath(fp)
			n[k] = name
		}
		if ok {
			if v, present := shadow[name]; !present || !sameValue(v, e.state.get(fp)) {
				ok = false
			}
		}
		seenNames[name] = struct{}{}
		fp.release()
	}
	return ok && len(seenNames) == len(shadow)
}

// dropElements deletes from s the elements of the array or table name at
// index n or above ("name.NNNN" and "name.NNNN.*") and returns how many.
func dropElements(s map[string]interface{}, name string, n uint32) int {
	removed := 0
	prefix := name + "."
	for k := range s {
		rest, ok := strings.CutPrefix(k, prefix)
		if !ok || len(rest) < 4 {
			continue
		}
		i, err := strconv.Atoi(rest[:4])
		if err != nil || len(rest) > 4 && rest[4] != '.' {
			continue
		}
		if uint32(i) >= n {
			delete(s, k)
			removed++
		}
	}
	return removed
}

// mapDiff describes up to 5 keys whose values differ between got and want.
func mapDiff(got, want map[string]interface{}) []string {
	var d []string
	for k, w := range want {
		if g, ok := got[k]; !ok || !sameValue(g, w) {
			d = append(d, fmt.Sprintf("%s: got %#v (present %v), want %#v", k, g, ok, w))
		}
	}
	for k, g := range got {
		if _, ok := want[k]; !ok {
			d = append(d, fmt.Sprintf("%s: got %#v, missing in Map", k, g))
		}
	}
	sort.Strings(d)
	if len(d) > 5 {
		d = d[:5]
	}
	return d
}

// sameValue is reflect.DeepEqual with NaN equal to NaN.
func sameValue(a, b interface{}) bool {
	if x, ok := a.(float32); ok {
		if y, ok := b.(float32); ok && x != x && y != y {
			return true
		}
	}
	return reflect.DeepEqual(a, b)
}
