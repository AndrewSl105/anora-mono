package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/andrewsl105/fleetpocket/server/internal/store"
	"github.com/andrewsl105/fleetpocket/server/internal/testutil"
)

func TestClaimNextPerHost(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, "demo", store.ProjectFields{}); err != nil {
		t.Fatal(err)
	}
	repo, err := st.AddRepository(ctx, store.Repository{ProjectID: "demo", FullName: "o/r", BaseBranch: "dev", InstallationID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(ctx, "mac", "Mac", []byte("h")); err != nil {
		t.Fatal(err)
	}
	one := 1
	if _, err := st.UpdateNode(ctx, "mac", store.NodeFields{MaxConcurrent: &one}); err != nil {
		t.Fatal(err)
	}
	mac := "mac"
	create := func(node *string) *store.Task {
		task, err := st.CreateTask(ctx, store.Task{ProjectID: "demo", RepositoryID: repo.ID, Title: "t", Prompt: "p", Source: "api", Node: node}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	m1, m2, s1 := create(&mac), create(&mac), create(nil)
	if m1.Node == nil || *m1.Node != "mac" || s1.Node != nil {
		t.Fatalf("node not stored: %v %v", m1.Node, s1.Node)
	}

	// The node is offline: only the server task is claimed.
	got, err := st.ClaimNext(ctx, 5, nil)
	if err != nil || got == nil || got.ID != s1.ID {
		t.Fatalf("offline node: claimed %+v, %v", got, err)
	}
	if got, _ := st.ClaimNext(ctx, 5, nil); got != nil {
		t.Fatalf("claimed a task of an offline node: %d", got.ID)
	}
	// Online with room for one.
	got, _ = st.ClaimNext(ctx, 5, []string{"mac"})
	if got == nil || got.ID != m1.ID {
		t.Fatalf("online node: claimed %+v", got)
	}
	if got, _ := st.ClaimNext(ctx, 5, []string{"mac"}); got != nil {
		t.Fatalf("node over its max_concurrent: claimed %d", got.ID)
	}
	// The server's limit does not count node tasks.
	if got, _ := st.ClaimNext(ctx, 1, nil); got != nil {
		t.Fatalf("server at its limit claimed %d", got.ID)
	}
	counts, _ := st.ActiveTasksByNode(ctx)
	if counts[""] != 1 || counts["mac"] != 1 {
		t.Fatalf("active counts %v", counts)
	}
	_ = m2

	if err := st.DeleteNode(ctx, "mac"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("delete with live tasks: %v", err)
	}
}

func TestNodeLifecycle(t *testing.T) {
	st := testutil.DB(t)
	ctx := context.Background()
	if _, err := st.CreateNode(ctx, "mac", "Mac", []byte("h1")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateNode(ctx, "mac", "Mac", []byte("h1")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate id: %v", err)
	}
	n, err := st.NodeSeen(ctx, "mac", []string{"claude", "codex"}, "darwin/arm64", "1.0")
	if err != nil || n.LastSeenAt == nil || len(n.Agents) != 2 || n.Platform != "darwin/arm64" {
		t.Fatalf("seen: %+v %v", n, err)
	}
	// A disconnect keeps the agents.
	n, _ = st.NodeSeen(ctx, "mac", nil, "", "")
	if len(n.Agents) != 2 {
		t.Fatalf("disconnect lost agents: %v", n.Agents)
	}
	if err := st.SetNodeToken(ctx, "mac", []byte("h2")); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.GetNode(ctx, "mac"); string(n.TokenHash) != "h2" {
		t.Fatalf("token not rotated")
	}

	// A project on the node goes back to the server when the node is removed.
	mac := "mac"
	if _, err := st.CreateProject(ctx, "p", store.ProjectFields{Node: &mac}); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetProject(ctx, "p"); p.Node == nil || *p.Node != "mac" {
		t.Fatalf("project node %v", p.Node)
	}
	if err := st.DeleteNode(ctx, "mac"); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetProject(ctx, "p"); p.Node != nil {
		t.Fatalf("project still on a removed node: %v", *p.Node)
	}
	server := "server"
	if p, err := st.UpdateProject(ctx, "p", store.ProjectFields{Node: &server}); err != nil || p.Node != nil {
		t.Fatalf("\"server\" must store NULL: %v %v", p, err)
	}
}
