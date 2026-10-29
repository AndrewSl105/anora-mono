package hosts

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeConn answers calls through a handler (nil: never answers).
type fakeConn struct {
	r      *Remote
	answer func(m Message) *Message
	mu     sync.Mutex
	closed bool
}

func (f *fakeConn) Send(_ context.Context, m Message) error {
	if f.answer != nil {
		if res := f.answer(m); res != nil {
			go f.r.Handle(*res)
		}
	}
	return nil
}

func (f *fakeConn) Close() {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}

func line(seq int) string {
	return fmt.Sprintf(`{"type":"tool_use","ts":"2026-10-29T10:00:00Z","seq":%d,"tool":"Edit","summary":"x"}`, seq)
}

func ok(m Message) *Message {
	return &Message{Op: OpResult, ID: m.ID, Result: []byte(`{"container":"c1"}`)}
}

func TestRemoteBacklogDoesNotBlockReadLoop(t *testing.T) {
	r := NewRemote("mac")
	c := &fakeConn{r: r, answer: ok}
	r.Install(c, &Hello{})
	runner, err := r.StartRunner(context.Background(), StartSpec{TaskID: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Far more lines than the event channel holds, nobody reading yet: Handle
	// (the connection's read loop) must not block.
	done := make(chan struct{})
	go func() {
		for i := 1; i <= 2000; i++ {
			r.Handle(Message{Op: OpLine, Container: "c1", Line: line(i)})
		}
		// A call result still gets through.
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("read loop blocked by an unread backlog")
	}
	// Two exit reports (hello and the exited message) must not panic.
	r.exit("c1", 0)
	r.Handle(Message{Op: OpExited, Container: "c1", Code: 3})
	n := 0
	for range runner.Events {
		n++
	}
	if n != 2000 {
		t.Fatalf("delivered %d events", n)
	}
	if code, _ := r.Wait(context.Background(), "c1"); code != 0 {
		t.Fatalf("exit code %d, want the first report", code)
	}
}

func TestRemoteReplacedConnectionFailsItsCalls(t *testing.T) {
	r := NewRemote("mac")
	old := &fakeConn{r: r} // never answers
	r.Install(old, &Hello{})
	errc := make(chan error, 1)
	go func() { errc <- r.Ping(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	r.Install(&fakeConn{r: r}, &Hello{})
	select {
	case err := <-errc:
		if !IsOffline(err) {
			t.Fatalf("call on the replaced connection: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call on a replaced connection never returned")
	}
	if !old.closed {
		t.Fatal("old connection not closed")
	}
	// Disconnect of the old connection does not take the new one offline.
	r.Disconnected(old)
	if !r.Online() {
		t.Fatal("new connection dropped by the old one's disconnect")
	}
}

func TestRemoteEventLogPages(t *testing.T) {
	r := NewRemote("mac")
	calls := 0
	r.Install(&fakeConn{r: r, answer: func(m Message) *Message {
		calls++
		var lines []string
		start := 1
		if calls == 2 {
			start = EventLogPage + 1
		}
		n := EventLogPage
		if calls == 2 {
			n = 5
		}
		for i := 0; i < n; i++ {
			lines = append(lines, line(start+i))
		}
		b := []byte("[")
		for i, l := range lines {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, []byte(fmt.Sprintf("%q", l))...)
		}
		b = append(b, ']')
		return &Message{Op: OpResult, ID: m.ID, Result: b}
	}}, &Hello{})
	lines, err := r.EventLog(context.Background(), 1, 0)
	if err != nil || len(lines) != EventLogPage+5 || calls != 2 {
		t.Fatalf("lines %d calls %d err %v", len(lines), calls, err)
	}
}
