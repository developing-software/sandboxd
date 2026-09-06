package sandbox

import (
	"testing"
	"time"
)

func TestRingKeepsTheMostRecentBytes(t *testing.T) {
	r := NewRing(10)
	r.Push([]byte("aaaa"))
	r.Push([]byte("bbbb"))
	r.Push([]byte("cccc"))
	if got := string(r.Snapshot()); got != "bbbbcccc" {
		t.Errorf("Snapshot() = %q", got)
	}
}

func TestRingKeepsTheTailOfAnOversizedChunk(t *testing.T) {
	r := NewRing(4)
	r.Push([]byte("0123456789"))
	if got := string(r.Snapshot()); got != "6789" {
		t.Errorf("Snapshot() = %q", got)
	}
}

func TestFanoutReplaysThenFollows(t *testing.T) {
	f := NewFanout(100)
	f.Emit([]byte("hi"))

	v, replay := f.Subscribe()
	if string(replay) != "hi" {
		t.Fatalf("replay = %q, want the tail so far", replay)
	}

	f.Emit([]byte("!"))
	if got := string(<-v.Bytes()); got != "!" {
		t.Errorf("live byte = %q", got)
	}
	if got := string(f.ring.Snapshot()); got != "hi!" {
		t.Errorf("ring = %q", got)
	}

	v.Close()
	f.Emit([]byte("x"))
	if _, open := <-v.Bytes(); open {
		t.Error("a closed viewer should see a closed channel")
	}
	if f.viewers() != 0 {
		t.Error("a closed viewer should be unsubscribed")
	}
}

func TestFanoutTracksActivity(t *testing.T) {
	f := NewFanout(100)
	before := f.LastActivity()
	time.Sleep(2 * time.Millisecond)

	f.Emit([]byte("out"))
	if !f.LastActivity().After(before) {
		t.Error("output should count as activity")
	}

	out := f.LastActivity()
	time.Sleep(2 * time.Millisecond)
	f.Touch()
	if !f.LastActivity().After(out) {
		t.Error("a keystroke should count as activity too")
	}
}

// The decided policy: a viewer that stops reading is dropped, not buffered. The ring owns
// catch-up, so the cost is one reconnect and not unbounded memory on the host.
func TestFanoutDropsAViewerThatFallsBehind(t *testing.T) {
	f := NewFanout(DefaultRingCap)
	v, _ := f.Subscribe()

	for range viewerQueue + 1 {
		f.Emit([]byte("x"))
	}
	if f.viewers() != 0 {
		t.Fatal("a viewer past its queue should have been dropped")
	}

	// Drain what it did receive; the channel is closed behind it.
	for range v.Bytes() { //nolint:revive // draining
	}
	if v.ClosedLocally() {
		t.Error("a dropped viewer did not close itself, and its stream must be announced")
	}
}

// Everything attached ends when the sandbox does, and each viewer can tell that apart
// from having hung up itself.
func TestFanoutCloseEndsEveryViewer(t *testing.T) {
	f := NewFanout(100)
	a, _ := f.Subscribe()
	b, _ := f.Subscribe()

	f.Close()
	for _, v := range []*Viewer{a, b} {
		if _, open := <-v.Bytes(); open {
			t.Error("viewer channel should be closed")
		}
		if v.ClosedLocally() {
			t.Error("the sandbox ended; the viewer did not close itself")
		}
	}

	// A viewer arriving after the end gets a closed channel rather than a hang.
	late, replay := f.Subscribe()
	if replay != nil {
		t.Error("nothing to replay from an ended sandbox")
	}
	if _, open := <-late.Bytes(); open {
		t.Error("a late viewer should see a closed channel")
	}
}
