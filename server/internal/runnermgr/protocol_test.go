package runnermgr

import (
	"os"
	"strings"
	"testing"
)

func TestParseEvent(t *testing.T) {
	ok := []string{
		`{"type":"ready","runner_version":"0.1.0","ts":"2026-10-14T15:08:42.564Z","seq":1}`,
		`{"type":"question","question_id":"q1","text":"Which?","options":["a","b"],"ts":"2026-10-14T15:08:42.573Z","seq":8}`,
		`{"type":"done","session_id":"s","summary":"x","no_changes":false,"cost_usd_total":0.01,"ts":"2026-10-14T15:08:42.614Z","seq":13}`,
		`{"type":"tool_result","tool":"Read","ok":false,"ts":"2026-10-14T15:08:42.614Z","seq":4}`,
	}
	for _, l := range ok {
		ev, err := ParseEvent([]byte(l))
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		if string(ev.Raw) != l {
			t.Fatalf("raw not kept")
		}
	}
	ev, _ := ParseEvent([]byte(ok[1]))
	if ev.QuestionID != "q1" || len(ev.Options) != 2 {
		t.Fatalf("bad question: %+v", ev)
	}
	ev, _ = ParseEvent([]byte(ok[3]))
	if ev.OK == nil || *ev.OK {
		t.Fatalf("ok=false must be preserved")
	}

	bad := map[string]string{
		`not json`: "invalid",
		`{"type":"boom","ts":"2026-10-14T15:08:42.564Z","seq":1}`:                "unknown",
		`{"type":"ready","ts":"2026-10-14T15:08:42.564Z"}`:                       "seq",
		`{"type":"ready","seq":3}`:                                               "ts",
		`{"type":"question","ts":"2026-10-14T15:08:42.564Z","seq":2}`:            "question",
		`{"type":"error","message":"x","ts":"2026-10-14T15:08:42.564Z","seq":2}`: "code",
	}
	for l, want := range bad {
		if _, err := ParseEvent([]byte(l)); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: got %v, want %q", l, err, want)
		}
	}
}

func TestEncodeCommand(t *testing.T) {
	b, err := EncodeCommand(NewAnswer("q1", "yes"))
	if err != nil || string(b) != `{"type":"answer","question_id":"q1","text":"yes"}`+"\n" {
		t.Fatalf("%q %v", b, err)
	}
	b, _ = EncodeCommand(NewRun(RunCommand{TaskID: "1", Prompt: "p"}))
	if !strings.HasPrefix(string(b), `{"type":"run","task_id":"1"`) {
		t.Fatalf("%s", b)
	}
}

// Every persisted runner event type has a typed schema in the API contract.
func TestOpenAPIHasEveryRunnerEvent(t *testing.T) {
	spec, err := os.ReadFile("../../../docs/openapi.yaml") // the repository root docs/
	if err != nil {
		t.Fatal(err)
	}
	for typ := range knownEvents {
		if typ == EvHeartbeat {
			continue // never persisted
		}
		if !strings.Contains(string(spec), "\n          "+typ+": '#/components/schemas/") {
			t.Errorf("docs/openapi.yaml: Event discriminator has no mapping for %q", typ)
		}
	}
}
