package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func signed(secret string, at time.Time, body string) (ts, sig string) {
	ts = strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + body))
	return ts, "v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerify(t *testing.T) {
	now := time.Unix(1_794_000_000, 0)
	body := `{"id":"d-1"}`
	ts, sig := signed("s3cret", now, body)

	if err := verify("s3cret", ts, sig, []byte(body), now); err != nil {
		t.Errorf("a good post was refused: %v", err)
	}
	if verify("other", ts, sig, []byte(body), now) == nil {
		t.Error("the wrong secret must not verify")
	}
	if verify("s3cret", ts, sig, []byte(body+" "), now) == nil {
		t.Error("a changed body must not verify")
	}
	if verify("s3cret", ts, sig, []byte(body), now.Add(10*time.Minute)) == nil {
		t.Error("an old post must be refused so it cannot be replayed")
	}
	if verify("s3cret", "", sig, []byte(body), now) == nil {
		t.Error("a missing timestamp must be refused")
	}
}
