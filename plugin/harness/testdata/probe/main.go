//go:build wasip1

// Command probe does what a broken plugin might, chosen by the "mode" setting.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/anant-barlota/girvi-sdk/plugin"
)

var sink [][]byte

//go:wasmexport on_event
func onEvent() {
	plugin.Run(func(h plugin.Host, in plugin.Input) plugin.Outcome {
		switch in.Settings["mode"] {
		case "panic":
			panic("plugin bug")
		case "spin":
			for {
			}
		case "sleep":
			time.Sleep(time.Hour)
		case "hog":
			for {
				block := make([]byte, 8<<20)
				for i := range block {
					block[i] = 1
				}
				sink = append(sink, block)
			}
		case "files":
			_, err := os.ReadFile("/etc/passwd")
			return plugin.OK(fmt.Sprintf("read=%v env=%d", err == nil, len(os.Environ())))
		case "store":
			n := 0
			for n < 2000 && h.StorageSet("k"+strconv.Itoa(n), []byte("v")) {
				n++
			}
			ok := h.WakeAt(time.Now().Add(time.Hour).Unix(), "later")
			return plugin.OK(fmt.Sprintf("stored=%d wake=%v", n, ok))
		case "flood":
			sent, last := 0, ""
			for range 25 {
				r := h.HTTP(plugin.Request{Method: "GET", URL: "https://api.example.com/"})
				if r.Error == "" {
					sent++
				}
				last = r.Error
			}
			return plugin.OK(fmt.Sprintf("sent=%d last=%s", sent, last))
		case "big":
			small := h.HTTP(plugin.Request{Method: "POST", URL: "https://api.example.com/", Body: make([]byte, 1<<20)})
			large := h.HTTP(plugin.Request{Method: "POST", URL: "https://api.example.com/", Body: make([]byte, 2<<20)})
			return plugin.OK(fmt.Sprintf("1MB=%q 2MB=%q", small.Error, large.Error))
		}
		h.Log("hello")
		return plugin.OK("fine")
	})
}

//go:wasmexport on_wake
func onWake() {
	plugin.Run(func(h plugin.Host, in plugin.Input) plugin.Outcome { return plugin.OK("woke with " + in.Wake) })
}

func main() {}
