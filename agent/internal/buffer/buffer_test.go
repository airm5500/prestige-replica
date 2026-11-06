package buffer

import "testing"

func TestRingBounded(t *testing.T) {
	r := New[int](3)
	for i := 1; i <= 5; i++ {
		r.Push(i)
	}
	if r.Len() != 3 || r.Dropped() != 2 {
		t.Fatalf("len=%d dropped=%d", r.Len(), r.Dropped())
	}
	got := r.Peek(10)
	if len(got) != 3 || got[0] != 3 || got[2] != 5 {
		t.Fatalf("peek=%v", got)
	}
	r.Drop(2)
	if r.Len() != 1 || r.Peek(1)[0] != 5 {
		t.Fatal("drop incorrect")
	}
}
