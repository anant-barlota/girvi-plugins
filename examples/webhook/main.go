//go:build wasip1

// The export Girvi calls. The plugin itself is in webhook.go.
package main

import "github.com/anant-barlota/girvi-sdk/plugin"

//go:wasmexport on_event
func onEvent() { plugin.Run(handle) }

func main() {}
