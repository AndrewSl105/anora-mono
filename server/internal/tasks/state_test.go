package tasks

import "testing"

func TestTransitions(t *testing.T) {
	ok := [][2]string{
		{Queued, Preparing}, {Preparing, Running}, {Running, WaitingInput}, {WaitingInput, Running},
		{WaitingInput, Paused}, {Paused, Queued}, {Running, Pushing}, {Pushing, PROpen},
		{PROpen, Completed}, {PROpen, Queued}, {Completed, Queued}, {Failed, Queued}, {Running, Completed},
	}
	for _, p := range ok {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s should be allowed", p[0], p[1])
		}
	}
	bad := [][2]string{
		{Queued, Running}, {Completed, Running}, {Paused, Running}, {Failed, PROpen}, {Cancelled, Running}, {PROpen, Running},
	}
	for _, p := range bad {
		if CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s should be rejected", p[0], p[1])
		}
	}
	for s := range transitions {
		for _, to := range transitions[s] {
			if !Valid(to) {
				t.Errorf("unknown target %s", to)
			}
		}
	}
}

func TestFrom(t *testing.T) {
	from := From(Paused)
	if len(from) != 2 {
		t.Fatalf("paused reachable from running and waiting_input, got %v", from)
	}
}

func TestMessageAction(t *testing.T) {
	cases := map[string]MessageAction{
		Running: MessageInject, WaitingInput: MessageInject,
		Paused: MessageNewRun, PROpen: MessageNewRun, Completed: MessageNewRun,
		Queued: MessageReject, Failed: MessageReject, Pushing: MessageReject, Cancelled: MessageReject,
	}
	for s, want := range cases {
		if got := MessageActionFor(s); got != want {
			t.Errorf("%s: got %v want %v", s, got, want)
		}
	}
}

func TestTerminalAndActive(t *testing.T) {
	if !IsTerminal(Completed) || IsTerminal(PROpen) {
		t.Fatal("terminal")
	}
	if !IsActive(Pushing) || IsActive(Paused) || IsActive(Queued) {
		t.Fatal("active")
	}
}
