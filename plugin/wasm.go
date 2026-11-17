//go:build wasip1

package plugin

import (
	"encoding/json"
	"unsafe"
)

// The whole wire contract with Girvi (import module girvi_v1). Every call moves
// one JSON message each way, and the plugin pulls and pushes the bytes itself,
// so Girvi never writes into the plugin's memory except where it is asked to.
// See the contract (girvi contract) for the exact shapes.

//go:wasmimport girvi_v1 input_len
func inputLen() uint32

//go:wasmimport girvi_v1 input_read
func inputRead(p unsafe.Pointer)

//go:wasmimport girvi_v1 output
func output(p unsafe.Pointer, n uint32)

// result_read copies out the result of the last http_request or storage_get.
//
//go:wasmimport girvi_v1 result_read
func resultRead(p unsafe.Pointer)

//go:wasmimport girvi_v1 log
func hostLog(p unsafe.Pointer, n uint32)

//go:wasmimport girvi_v1 http_request
func httpRequest(p unsafe.Pointer, n uint32) uint32

//go:wasmimport girvi_v1 storage_get
func storageGet(kp unsafe.Pointer, kn uint32) int32

//go:wasmimport girvi_v1 storage_set
func storageSet(kp unsafe.Pointer, kn uint32, vp unsafe.Pointer, vn uint32) int32

//go:wasmimport girvi_v1 storage_delete
func storageDelete(kp unsafe.Pointer, kn uint32) int32

//go:wasmimport girvi_v1 wake_at
func wakeAt(unixSeconds int64, pp unsafe.Pointer, pn uint32) int32

func ptr(b []byte) unsafe.Pointer { return unsafe.Pointer(unsafe.SliceData(b)) }

func result(n int) []byte {
	out := make([]byte, n)
	if n > 0 {
		resultRead(ptr(out))
	}
	return out
}

type wasmHost struct{}

func (wasmHost) Log(msg string) {
	b := []byte(msg)
	hostLog(ptr(b), uint32(len(b)))
}

func (wasmHost) HTTP(req Request) Response {
	in, err := json.Marshal(req)
	if err != nil {
		return Response{Error: err.Error()}
	}
	var resp Response
	if err := json.Unmarshal(result(int(httpRequest(ptr(in), uint32(len(in))))), &resp); err != nil {
		return Response{Error: "bad response from host: " + err.Error()}
	}
	return resp
}

func (wasmHost) StorageGet(key string) ([]byte, bool) {
	k := []byte(key)
	n := storageGet(ptr(k), uint32(len(k)))
	if n < 0 {
		return nil, false
	}
	return result(int(n)), true
}

func (wasmHost) StorageSet(key string, value []byte) bool {
	k := []byte(key)
	return storageSet(ptr(k), uint32(len(k)), ptr(value), uint32(len(value))) == 0
}

func (wasmHost) StorageDelete(key string) bool {
	k := []byte(key)
	return storageDelete(ptr(k), uint32(len(k))) == 0
}

func (wasmHost) WakeAt(unixSeconds int64, payload string) bool {
	p := []byte(payload)
	return wakeAt(unixSeconds, ptr(p), uint32(len(p))) == 0
}

// Run handles one call. Call it from each exported function:
//
//	//go:wasmexport on_event
//	func onEvent() { plugin.Run(handleEvent) }
//
//	//go:wasmexport on_wake
//	func onWake() { plugin.Run(handleWake) }
//
// A panic in the handler ends the call with no answer, which Girvi records.
func Run(h Handler) {
	buf := make([]byte, inputLen())
	if len(buf) > 0 {
		inputRead(ptr(buf))
	}
	var in Input
	var out Outcome
	if err := json.Unmarshal(buf, &in); err != nil {
		out = Failed("could not read the input: " + err.Error())
	} else {
		out = h(wasmHost{}, in)
	}
	b, _ := json.Marshal(out)
	output(ptr(b), uint32(len(b)))
}
