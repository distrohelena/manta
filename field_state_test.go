package manta

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCloneIsolatesMutableLeaves verifies entities cloned from a baseline
// template cannot affect the template or each other: not by mutating a vector
// or blob returned from get (shared leaves are handed out as copies), and not
// by setting values (nested states are copied on first write).
func TestCloneIsolatesMutableLeaves(t *testing.T) {
	assert := assert.New(t)

	at := func(path ...int32) *fieldPath {
		fp := &fieldPath{}
		copy(fp.path[:], path)
		fp.last = int32(len(path) - 1)
		return fp
	}

	tmpl := newFieldState()
	tmpl.set(at(0), floatsCell([]float32{1, 2, 3}))
	tmpl.set(at(1), bytesCell([]byte{4, 5}))
	tmpl.set(at(2, 0), floatsCell([]float32{6, 7}))
	tmpl.set(at(3), int32Cell(8))
	tmpl.set(at(4, 0, 0), int32Cell(9))
	tmpl.share()

	a, b := tmpl.clone(), tmpl.clone()

	// Mutate everything handed out by a.
	a.get(at(0)).([]float32)[0] = 100
	a.get(at(1)).([]byte)[0] = 100
	a.get(at(2, 0)).([]float32)[0] = 100
	a.set(at(3), int32Cell(100))
	a.set(at(2, 1), int32Cell(100))
	a.set(at(4, 0, 1), int32Cell(100))

	for name, s := range map[string]*fieldState{"template": tmpl, "sibling": b} {
		assert.Equal([]float32{1, 2, 3}, s.get(at(0)), name)
		assert.Equal([]byte{4, 5}, s.get(at(1)), name)
		assert.Equal([]float32{6, 7}, s.get(at(2, 0)), name)
		assert.Equal(int32(8), s.get(at(3)), name)
		assert.Nil(s.get(at(2, 1)), name)
		assert.Equal(int32(9), s.get(at(4, 0, 0)), name)
		assert.Nil(s.get(at(4, 0, 1)), name)
	}

	// The clone's own shared leaves are unchanged too; its sets took effect.
	assert.Equal([]float32{1, 2, 3}, a.get(at(0)))
	assert.Equal([]byte{4, 5}, a.get(at(1)))
	assert.Equal([]float32{6, 7}, a.get(at(2, 0)))
	assert.Equal(int32(100), a.get(at(3)))
	assert.Equal(int32(100), a.get(at(2, 1)))
	assert.Equal(int32(9), a.get(at(4, 0, 0)))
	assert.Equal(int32(100), a.get(at(4, 0, 1)))

	// A freshly decoded value replaces the shared mark.
	v := []float32{9, 9}
	a.set(at(0), floatsCell(v))
	v[0] = 10
	assert.Equal([]float32{10, 9}, a.get(at(0)))
}

// TestSetLengthTruncates verifies that writing a variable array or table
// length onto an existing nested state drops the elements at or past the new
// length (the client resizes the vector), keeps the ones before it, records
// the length, and never touches a baseline template it is shared with.
func TestSetLengthTruncates(t *testing.T) {
	assert := assert.New(t)

	at := func(path ...int32) *fieldPath {
		fp := &fieldPath{}
		copy(fp.path[:], path)
		fp.last = int32(len(path) - 1)
		return fp
	}

	// setLength writes a length as readFields does.
	setLength := func(s *fieldState, fp *fieldPath, n uint32) {
		if s.set(fp, smallUint64Cell(uint64(n))) {
			s.resizeAt(fp, n)
		}
	}

	tmpl := newFieldState()
	// Variable array at 0: length leaf first, then three elements.
	setLength(tmpl, at(0), 3)
	tmpl.set(at(0, 0), int32Cell(10))
	tmpl.set(at(0, 1), int32Cell(11))
	tmpl.set(at(0, 2), int32Cell(12))
	// Variable table at 1: two element tables with one field each.
	setLength(tmpl, at(1), 2)
	tmpl.set(at(1, 0, 0), int32Cell(20))
	tmpl.set(at(1, 1, 0), int32Cell(21))
	tmpl.share()

	n, ok := tmpl.lengthAt(at(0))
	assert.True(ok)
	assert.Equal(uint32(3), n, "length kept when the leaf becomes a nested state")

	a := tmpl.clone()
	setLength(a, at(0), 1)
	setLength(a, at(1), 1)

	assert.Equal(int32(10), a.get(at(0, 0)))
	assert.Nil(a.get(at(0, 1)))
	assert.Nil(a.get(at(0, 2)))
	assert.Equal(int32(20), a.get(at(1, 0, 0)))
	assert.Nil(a.get(at(1, 1, 0)))
	n, ok = a.lengthAt(at(0))
	assert.True(ok)
	assert.Equal(uint32(1), n)
	n, ok = a.lengthAt(at(1))
	assert.True(ok)
	assert.Equal(uint32(1), n)

	// The template is untouched.
	assert.Equal(int32(12), tmpl.get(at(0, 2)))
	assert.Equal(int32(21), tmpl.get(at(1, 1, 0)))
	n, _ = tmpl.lengthAt(at(0))
	assert.Equal(uint32(3), n)

	// Growing again keeps the surviving element; new ones appear when set.
	setLength(a, at(0), 4)
	assert.Equal(int32(10), a.get(at(0, 0)))
	assert.Nil(a.get(at(0, 1)))
	a.set(at(0, 3), int32Cell(13))
	assert.Equal(int32(13), a.get(at(0, 3)))
	n, _ = a.lengthAt(at(0))
	assert.Equal(uint32(4), n)

	// A plain field has no length.
	a.set(at(2), int32Cell(5))
	_, ok = a.lengthAt(at(2))
	assert.False(ok)
}
