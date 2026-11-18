//go:build wasip1

// Command hostile does what an attacker's or a badly broken plugin would,
// chosen by the "mode" setting, so the tests can show the sandbox stops it.
// With no mode it behaves, as the control.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/anant-barlota/girvi-sdk/plugin"
)

// The host functions, called directly, to pass what the SDK never would.

//go:wasmimport girvi_v1 input_len
func inputLen() uint32

//go:wasmimport girvi_v1 input_read
func inputRead(ptr uint32)

//go:wasmimport girvi_v1 output
func output(ptr, n uint32)

//go:wasmimport girvi_v1 log
func hostLog(ptr, n uint32)

//go:wasmimport girvi_v1 result_read
func resultRead(ptr uint32)

func addr(b []byte) uint32 { return uint32(uintptr(unsafe.Pointer(&b[0]))) }

// outsideMemory is an address past the end of any 64 MB memory.
const outsideMemory = 0xFFFF_FF00

var sink [][]byte

// mode reads the setting before plugin.Run does, for the modes that must end
// the call without the SDK writing an outcome.
func mode() string {
	buf := make([]byte, inputLen())
	inputRead(addr(buf))
	var in struct {
		Settings map[string]string `json:"settings"`
	}
	_ = json.Unmarshal(buf, &in)
	return in.Settings["mode"]
}

//go:wasmexport on_event
func onEvent() {
	switch mode() {
	case "bad-pointer":
		hostLog(outsideMemory, 64)
	case "huge-outcome":
		out, _ := json.Marshal(plugin.OK(strings.Repeat("x", 100<<10)))
		output(addr(out), uint32(len(out)))
		return
	}
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
		case "stderr":
			fmt.Fprint(os.Stderr, strings.Repeat("x", 100<<10))
			panic("after shouting")
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
		case "big-storage":
			longKey := h.StorageSet(strings.Repeat("k", 257), []byte("v"))
			bigValue := h.StorageSet("big", make([]byte, 1<<20+1))
			emptyKey := h.StorageSet("", []byte("v"))
			return plugin.OK(fmt.Sprintf("long key=%v big value=%v empty key=%v", longKey, bigValue, emptyKey))
		case "wakes":
			now := h.WakeAt(0, "now")
			never := h.WakeAt(1<<62, "never")
			big := h.WakeAt(time.Now().Add(time.Hour).Unix(), strings.Repeat("p", 64<<10+1))
			return plugin.OK(fmt.Sprintf("now=%v never=%v big payload=%v", now, never, big))
		case "logs":
			for range 100 {
				h.Log(strings.Repeat("l", 1000))
			}
		case "long-note":
			return plugin.OK(strings.Repeat("n", 5000))
		case "twice":
			out, _ := json.Marshal(plugin.Failed("first"))
			output(addr(out), uint32(len(out)))
			return plugin.OK("second")
		case "no-result":
			buf := make([]byte, 16)
			resultRead(addr(buf))
			return plugin.OK("read nothing")
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
		case "elsewhere":
			r := h.HTTP(plugin.Request{Method: "GET", URL: "https://attacker.example.net/"})
			return plugin.OK(r.Error)
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
