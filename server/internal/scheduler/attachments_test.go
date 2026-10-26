package scheduler_test

import (
	"strings"
	"testing"

	"github.com/andrewsl105/fleetpocket/server/internal/scheduler"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
)

func TestAttachmentBlock(t *testing.T) {
	if got := scheduler.AttachmentBlock("claude", nil, nil); got != "" {
		t.Fatalf("no attachments: %q", got)
	}
	cur := []*store.Attachment{{ID: 7, Name: "mock.png", Mime: "image/png", Width: 800, Height: 600}}
	earlier := []*store.Attachment{{ID: 3, Name: "old photo.jpeg", Mime: "image/jpeg", Width: 10, Height: 20}}
	got := scheduler.AttachmentBlock("claude", cur, earlier)
	for _, want := range []string{
		"Attached images (open each with the Read tool",
		"- /attachments/7.png (mock.png, 800x600)",
		"Earlier attachments of this task",
		"- /attachments/3.jpg (old photo.jpeg, 10x20)",
		"/attachments is read-only",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("block lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "7.png") > strings.Index(got, "3.jpg") {
		t.Error("current attachments must come before earlier ones")
	}
	// A resume after a question has no new images but still lists the earlier ones.
	if got := scheduler.AttachmentBlock("claude", nil, earlier); strings.Contains(got, "Attached images") || !strings.Contains(got, "3.jpg") {
		t.Fatalf("earlier only: %q", got)
	}
}

func TestAttachmentBlockOtherAgents(t *testing.T) {
	cur := []*store.Attachment{{ID: 1, Name: "a.png", Mime: "image/png", Width: 1, Height: 1}}
	got := scheduler.AttachmentBlock("codex", cur, cur)
	if strings.Contains(got, "Read tool") || !strings.Contains(got, "Attached images (included with this message):\n- /attachments/1.png") {
		t.Fatalf("codex block:\n%s", got)
	}
}

func TestSystemPromptByAgent(t *testing.T) {
	repo := &store.Repository{FullName: "o/r", BaseBranch: "main"}
	if got := scheduler.SystemPrompt(&store.Task{ID: 1, Agent: "claude"}, repo, "b", ""); !strings.Contains(got, "AskUserQuestion") {
		t.Fatalf("claude prompt: %s", got)
	}
	got := scheduler.SystemPrompt(&store.Task{ID: 1, Agent: "cursor"}, repo, "b", "")
	if strings.Contains(got, "AskUserQuestion") || !strings.Contains(got, "list open questions in your summary") {
		t.Fatalf("cursor prompt: %s", got)
	}
}
