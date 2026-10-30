package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)

	if _, err := s.User(ctx, 42); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	if err := s.CreateUser(ctx, 42, "Alice", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAPIKey(ctx, 42, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWizard(ctx, 42, `{"step":"name"}`); err != nil {
		t.Fatal(err)
	}
	u, err := s.User(ctx, 42)
	if err != nil || u.Name != "Alice" || len(u.APIKey) != 3 || u.Wizard != `{"step":"name"}` || !u.CreatedAt.Equal(now) {
		t.Fatalf("user = %+v, %v", u, err)
	}

	id, err := s.CreateWatch(ctx, Watch{ChatID: 42, Config: "name: A", EveryDays: 3, NextRun: now, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateWatch(ctx, Watch{ChatID: 42, Config: "name: B", EveryDays: 1, NextRun: now.Add(time.Hour), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueWatches(ctx, now)
	if err != nil || len(due) != 1 || due[0].ID != id {
		t.Fatalf("due = %+v, %v", due, err)
	}

	w := due[0]
	w.History, w.LastRun, w.NextRun, w.LastError = `{"days":[]}`, now, now.AddDate(0, 0, 3), "boom"
	if err := s.SaveWatch(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, err := s.Watch(ctx, 42, id)
	if err != nil || got.History != `{"days":[]}` || !got.NextRun.Equal(now.AddDate(0, 0, 3)) || got.LastError != "boom" {
		t.Fatalf("watch = %+v, %v", got, err)
	}
	if _, err := s.Watch(ctx, 7, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("another user must not see the watch")
	}
	if err := s.DeleteWatch(ctx, 7, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("another user must not delete the watch")
	}

	if err := s.SetMeta(ctx, "offset", "10"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Meta(ctx, "offset"); v != "10" {
		t.Fatalf("meta = %q", v)
	}
	if err := s.Backup(ctx, filepath.Join(dir, "backup.db")); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteUser(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if ws, _ := s.Watches(ctx, 42); len(ws) != 0 {
		t.Fatalf("watches not deleted with the user: %+v", ws)
	}
}
