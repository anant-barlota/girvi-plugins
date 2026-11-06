package queue

import (
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Queue {
	t.Helper()
	q, err := Open(filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

func TestAddRecognizesAKnownDeliveryID(t *testing.T) {
	q := open(t)
	now := time.Now()
	created, _, err := q.Add("d1", []byte("a"), now.Add(time.Hour), now)
	if err != nil || !created {
		t.Fatalf("first add: created=%v err=%v", created, err)
	}
	created, existing, err := q.Add("d1", []byte("b"), now.Add(time.Hour), now)
	if err != nil || created {
		t.Fatalf("second add: created=%v err=%v", created, err)
	}
	if string(existing.Raw) != "a" {
		t.Errorf("the first copy must win, got %q", existing.Raw)
	}
}

func TestNextDueRespectsTimeAndState(t *testing.T) {
	q := open(t)
	now := time.Now()
	_, _, _ = q.Add("later", nil, now.Add(time.Hour), now)
	_, _, _ = q.Add("now", nil, now.Add(time.Hour), now)
	if err := q.MarkRetry("later", "boom", now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}

	row, ok, _ := q.NextDue(now)
	if !ok || row.DeliveryID != "now" {
		t.Fatalf("NextDue = %v %v; want the item that is due", row.DeliveryID, ok)
	}
	if err := q.MarkDelivered("now", "ref", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := q.NextDue(now); ok {
		t.Error("nothing should be due: one is delivered, one waits for its retry")
	}
	wake, has, _ := q.NextWake()
	if !has || wake.Before(now.Add(30*time.Second)) {
		t.Errorf("NextWake = %v %v; want about a minute out", wake, has)
	}
	row, _, _ = q.Get("later")
	if row.Attempts != 1 || row.LastError != "boom" {
		t.Errorf("retry bookkeeping wrong: %+v", row)
	}
}

func TestPurgeKeepsUnfinishedItems(t *testing.T) {
	q := open(t)
	old := time.Now().Add(-10 * 24 * time.Hour)
	_, _, _ = q.Add("done", nil, old.Add(time.Hour), old)
	_, _, _ = q.Add("open", nil, old.Add(time.Hour), old)
	_ = q.MarkDelivered("done", "r", old)

	n, err := q.Purge(time.Now().Add(-8 * 24 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("purge removed %d (err %v); want 1", n, err)
	}
	if _, ok, _ := q.Get("done"); ok {
		t.Error("finished item should be gone")
	}
	if _, ok, _ := q.Get("open"); !ok {
		t.Error("unfinished item must stay")
	}
}

func TestUpdatesComeBackInOrderAndCanBeDeleted(t *testing.T) {
	q := open(t)
	now := time.Now()
	for _, id := range []string{"a", "b", "c"} {
		_ = q.AddUpdate(Update{DeliveryID: id, State: 4, At: now})
	}
	ups, _ := q.PendingUpdates(10)
	if len(ups) != 3 || ups[0].DeliveryID != "a" || ups[2].DeliveryID != "c" {
		t.Fatalf("updates out of order: %+v", ups)
	}
	_ = q.DeleteUpdate(ups[0].ID)
	ups, _ = q.PendingUpdates(10)
	if len(ups) != 2 || ups[0].DeliveryID != "b" {
		t.Errorf("delete did not remove the first update: %+v", ups)
	}
}
