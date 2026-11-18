//go:build wasip1

// The exports Girvi calls. Everything else is in handler.go, which has no
// WebAssembly in it, so it is tested with plain go test.
package main

import "github.com/anant-barlota/girvi-sdk/plugin"

//go:wasmexport on_event
func onEvent() { plugin.Run(handle) }

func main() {}
