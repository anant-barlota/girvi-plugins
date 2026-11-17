// Package sandboxtest checks that a sandbox.Store keeps and counts the way
// the sandbox's limits assume. Girvi runs it against its database store, and
// this module against the memory store, so the two cannot drift apart.
package sandboxtest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/anant-barlota/girvi-sdk/plugin/sandbox"
)

// TestStore runs every check against a fresh, empty store from newStore.
func TestStore(t *testing.T, newStore func(t *testing.T) sandbox.Store) {
	ctx := context.Background()

	t.Run("missing", func(t *testing.T) {
		s := newStore(t)
		if _, ok, err := s.Get(ctx, "nothing"); ok || err != nil {
			t.Fatalf("a missing key: found=%v err=%v", ok, err)
		}
		if err := s.Delete(ctx, "nothing"); err != nil {
			t.Fatalf("deleting a missing key: %v", err)
		}
	})

	t.Run("set, replace, delete", func(t *testing.T) {
		s := newStore(t)
		for _, v := range [][]byte{[]byte("one"), []byte("two")} {
			if err := s.Set(ctx, "k", v); err != nil {
				t.Fatal(err)
			}
			got, ok, err := s.Get(ctx, "k")
			if err != nil || !ok || !bytes.Equal(got, v) {
				t.Fatalf("get after set %q: %q found=%v err=%v", v, got, ok, err)
			}
		}
		if err := s.Delete(ctx, "k"); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.Get(ctx, "k"); ok {
			t.Fatal("still there after delete")
		}
	})

	t.Run("empty and binary values", func(t *testing.T) {
		s := newStore(t)
		bin := []byte{0, 1, 2, 0xff, 0}
		for key, v := range map[string][]byte{"empty": {}, "binary": bin} {
			if err := s.Set(ctx, key, v); err != nil {
				t.Fatal(err)
			}
			got, ok, err := s.Get(ctx, key)
			if err != nil || !ok || !bytes.Equal(got, v) {
				t.Fatalf("%s: %v found=%v err=%v", key, got, ok, err)
			}
		}
	})

	t.Run("usage is keys and bytes", func(t *testing.T) {
		s := newStore(t)
		// "₹" is three bytes: sizes are bytes, not characters.
		_ = s.Set(ctx, "a", []byte("12345"))
		_ = s.Set(ctx, "₹", []byte("₹"))
		_ = s.Set(ctx, "a", []byte("123")) // a replacement counts once
		keys, size, err := s.Usage(ctx)
		if err != nil || keys != 2 || size != int64(1+3+3+3) {
			t.Fatalf("usage: keys=%d size=%d err=%v, want 2 and 10", keys, size, err)
		}
	})

	t.Run("wake-ups are counted", func(t *testing.T) {
		s := newStore(t)
		for i := range 3 {
			if n, err := s.PendingWakeUps(ctx); err != nil || n != int64(i) {
				t.Fatalf("pending: %d err=%v, want %d", n, err, i)
			}
			if err := s.WakeAt(ctx, time.Now().Add(time.Hour), "later"); err != nil {
				t.Fatal(err)
			}
		}
	})
}
