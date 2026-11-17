//go:build wasip1

// The export Girvi calls. The plugin itself is in webhook.go.
package main

import "github.com/anant-barlota/girvi-plugins/sdk"

//go:wasmexport on_event
func onEvent() { sdk.Run(handle) }

func main() {}
