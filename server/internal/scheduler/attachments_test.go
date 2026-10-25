package scheduler_test

import (
	"strings"
	"testing"

	"github.com/andrewsl105/fleetpocket/server/internal/scheduler"
	"github.com/andrewsl105/fleetpocket/server/internal/store"
)

func TestAttachmentBlock(t *testing.T) {
	if got := scheduler.AttachmentBlock(nil, nil); got != "" {
		t.Fatalf("no attachments: %q", got)
	}
	cur := []*store.Attachment{{ID: 7, Name: "mock.png", Mime: "image/png", Width: 800, Height: 600}}
	earlier := []*store.Attachment{{ID: 3, Name: "old photo.jpeg", Mime: "image/jpeg", Width: 10, Height: 20}}
	got := scheduler.AttachmentBlock(cur, earlier)
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
	if got := scheduler.AttachmentBlock(nil, earlier); strings.Contains(got, "Attached images") || !strings.Contains(got, "3.jpg") {
		t.Fatalf("earlier only: %q", got)
	}
}
