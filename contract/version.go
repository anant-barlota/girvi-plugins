package contract

import "runtime/debug"

// ModulePath is this repository's Go module.
const ModulePath = "github.com/anant-barlota/girvi-plugins"

// Version is the version of this module the running program was built with:
// for Girvi, the contract and sandbox it runs plugins with; for the CLI and
// the MCP server, the ones they test against. Two programs with the same
// version run a plugin the same way. It is "" for a build from a checkout.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	if info.Main.Path == ModulePath && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	for _, d := range info.Deps {
		if d.Path == ModulePath && d.Version != "(devel)" {
			if d.Replace != nil {
				return ""
			}
			return d.Version
		}
	}
	return ""
}
