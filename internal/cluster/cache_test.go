package cluster

import (
	"sync"
	"testing"
	"time"
)

func TestBroadcasterDeliversToAllSubscribers(t *testing.T) {
	b := newBroadcaster()
	defer b.close()

	_, a := b.Subscribe(4)
	_, c := b.Subscribe(4)

	b.Publish(Event{Type: EventAdded})

	for i, ch := range []<-chan Event{a, c} {
		select {
		case ev := <-ch:
			if ev.Type != EventAdded {
				t.Errorf("subscriber %d got %s", i, ev.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d received nothing", i)
		}
	}
}

func TestBroadcasterDropsSlowSubscriber(t *testing.T) {
	// A subscriber that stops reading must not be able to stall the informer
	// for everyone else. It gets dropped and told to reload.
	b := newBroadcaster()
	defer b.close()

	id, slow := b.Subscribe(1)
	_, fast := b.Subscribe(16)

	for i := 0; i < 8; i++ {
		b.Publish(Event{Type: EventModified})
	}

	// The fast subscriber still received everything.
	got := 0
	for len(fast) > 0 {
		<-fast
		got++
	}
	if got != 8 {
		t.Errorf("fast subscriber got %d events, want 8", got)
	}

	// The slow one was closed rather than blocking the publisher.
	drained := 0
	for {
		_, ok := <-slow
		if !ok {
			break
		}
		drained++
		if drained > 10 {
			t.Fatal("slow subscriber channel was never closed")
		}
	}

	if b.Count() != 1 {
		t.Errorf("expected the slow subscriber to be unsubscribed, count=%d", b.Count())
	}
	// Unsubscribing an already-dropped id must not panic on a double close.
	b.Unsubscribe(id)
}

func TestBroadcasterUnsubscribeClosesChannel(t *testing.T) {
	b := newBroadcaster()
	defer b.close()

	id, ch := b.Subscribe(1)
	b.Unsubscribe(id)

	if _, ok := <-ch; ok {
		t.Error("channel should be closed after unsubscribe")
	}
	if b.Count() != 0 {
		t.Errorf("count = %d, want 0", b.Count())
	}
}

func TestBroadcasterCloseIsIdempotentUnderConcurrency(t *testing.T) {
	b := newBroadcaster()
	var wg sync.WaitGroup

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, _ := b.Subscribe(2)
			b.Publish(Event{Type: EventModified})
			b.Unsubscribe(id)
		}()
	}
	wg.Wait()
	b.close()
	b.close() // must not panic

	// Subscribing after close hands back a closed channel rather than a
	// channel that will never receive.
	_, ch := b.Subscribe(1)
	if _, ok := <-ch; ok {
		t.Error("subscribing to a closed broadcaster should yield a closed channel")
	}
}

func TestInformerEntryIdleTracking(t *testing.T) {
	e := &informerEntry{}
	e.touch()

	if idle := e.idleFor(); idle > time.Second {
		t.Errorf("freshly touched entry reports idle=%s", idle)
	}
}
