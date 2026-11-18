// Command receiver is a small webhook receiver for trying the Girvi webhook
// plugin. It checks the signature and the age of each post the way a real
// receiver should, prints what arrived, and answers 200.
//
//	go run ./examples/receiver -addr :9090 -secret s3cret
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

func main() {
	addr := flag.String("addr", ":9090", "address to listen on")
	secret := flag.String("secret", "", "the signing secret set in Girvi; leave empty to skip checking")
	flag.Parse()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))

		if *secret != "" {
			if err := verify(*secret, r.Header.Get("X-Girvi-Timestamp"), r.Header.Get("X-Girvi-Signature"), body, time.Now()); err != nil {
				log.Printf("REFUSED %s: %v", r.Header.Get("X-Girvi-Delivery"), err)
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
		}

		var pretty bytes.Buffer
		_ = json.Indent(&pretty, body, "  ", "  ")
		log.Printf("%s  delivery=%s  signed=%v\n  %s", r.Header.Get("X-Girvi-Event"), r.Header.Get("X-Girvi-Delivery"), *secret != "", pretty.String())
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("listening on %s (signature checking %s)", *addr, map[bool]string{true: "on", false: "off"}[*secret != ""])
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// verify is what every receiver should do: recompute the signature from the
// timestamp and the exact body, compare in constant time, and refuse old posts
// so a captured one cannot be replayed.
func verify(secret, timestamp, signature string, body []byte, now time.Time) error {
	sent, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("missing or bad timestamp")
	}
	if age := now.Sub(time.Unix(sent, 0)); age > 5*time.Minute || age < -5*time.Minute {
		return fmt.Errorf("timestamp is too old or too new (%s)", age.Round(time.Second))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + string(body)))
	want := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(signature)) {
		return fmt.Errorf("signature does not match")
	}
	return nil
}
