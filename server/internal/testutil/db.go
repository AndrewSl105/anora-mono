// Package testutil provides a throwaway database per test. Tests that need
// Postgres are skipped unless ANORA_TEST_DATABASE_URL is set.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/andrewsl105/anora/server/internal/store"
)

func DB(t *testing.T) *store.Store {
	t.Helper()
	admin := os.Getenv("ANORA_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("ANORA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "anora_test_" + hex.EncodeToString(b)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create db: %v", err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Task ids name Docker resources (anora-task-<id>); a random start keeps
	// test packages that run in parallel from colliding.
	start := 1_000_000 + int64(b[0])<<16 | int64(b[1])<<8 | int64(b[2])
	if _, err := st.Pool.Exec(ctx, fmt.Sprintf("ALTER SEQUENCE tasks_id_seq RESTART WITH %d", start)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		_, _ = conn.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		conn.Close(context.Background())
	})
	return st
}
