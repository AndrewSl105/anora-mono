// Package testutil provides a throwaway database per test. Tests that need
// Postgres are skipped unless ANORA_TEST_DATABASE_URL is set.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/andrewsl105/anora-runner/internal/store"
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
	t.Cleanup(func() {
		st.Close()
		_, _ = conn.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		conn.Close(context.Background())
	})
	return st
}
