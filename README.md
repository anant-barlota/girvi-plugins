# Girvi plugins

Everything needed to write a plugin for [Girvi](https://girvi.app), a pawn-shop app: the contract, a Go SDK, a
command-line tool that makes, builds and tests plugins, and an MCP server so an AI assistant can do the same.

A plugin is a small add-on that acts when a loan or payment is saved: posts it to a webhook, texts the customer,
exports it to accounting. It is a WebAssembly module that Girvi runs in a sandbox, so it cannot read files or
Girvi's records, and can reach only the hosts its manifest names. Shops install a plugin from a file, see what it
may do, approve it, and turn it on per firm.

> **Status:** `girvi/1` is new and not yet frozen. Nothing here is tagged for it yet; until it is, use a checkout
> (see [Without a release](#without-a-release)). The older process-based SDK is at tags `v0.1.0-alpha.*`.

## Contents

| Path | What it is |
|---|---|
| [`contract/CONTRACT.md`](contract/CONTRACT.md) | The `girvi/1` contract: manifest, exports, host functions, outcomes, limits, events. Start here. |
| [`contract`](contract) | The same as Go: limits, the host module, samples, and what a plugin is given of an event. Girvi imports it. |
| [`contract/plugin.schema.json`](contract/plugin.schema.json) | JSON schema of `plugin.json`, for editor completion. |
| [`contract/fixtures`](contract/fixtures) | A sample of every event, as Girvi sends it. |
| [`sdk`](sdk) | The Go SDK. Handlers are ordinary functions; `sdk/sdktest` is a fake host for `go test`. |
| [`cmd/girvi-plugin`](cmd/girvi-plugin) | The CLI: `new`, `validate`, `build`, `test`, `contract`, `version`. |
| [`cmd/girvi-plugin-mcp`](cmd/girvi-plugin-mcp) | The same, as MCP tools for an AI assistant. |
| [`sandbox`](sandbox) | Girvi's plugin runtime: host functions, limits, deadline, what counts as an answer. Girvi runs plugins with it. |
| [`harness`](harness) | The same runtime with storage in memory and scripted HTTP, so a test sends nothing. Used by `test`. |
| [`manifest`](manifest) | Reads and checks `plugin.json`, bundles and their imports, and which hosts a plugin may reach, with Girvi's own rules. |
| [`examples/webhook`](examples/webhook) | A complete plugin: posts loan events as signed JSON to a URL each firm chooses. |

## Write a plugin

You need Go 1.25 or later.

```sh
go install github.com/anant-barlota/girvi-plugins/cmd/girvi-plugin@<version>

girvi-plugin new ./sms --id your-name/sms
cd sms
go test ./...                    # the handler, natively
girvi-plugin build               # dist/sms-0.1.0.zip, the bundle to install
girvi-plugin test                # run it in the sandbox against a sample event
```

`new` makes a plugin that builds and passes its tests as it is. Then:

1. **Edit `plugin.json`**: the events it hears, the customer details it needs, the hosts it may reach, its settings
   form. Unknown fields are refused, so a typo is caught at once.
2. **Edit `handler.go`**:

   ```go
   func handle(h sdk.Host, in sdk.Input) sdk.Outcome {
       e, err := in.LoanCreated()
       if err != nil {
           return sdk.Failed(err.Error())
       }
       resp := h.HTTP(sdk.Request{Method: "POST", URL: in.Settings["url"], Body: body(e)})
       switch {
       case resp.Error != "" || resp.Status >= 500:
           return sdk.Retry(300, "the receiver is unavailable")
       case resp.Status == 401:
           return sdk.Blocked("check the token in the settings")
       case resp.Status >= 400:
           return sdk.Failed(fmt.Sprintf("refused (HTTP %d)", resp.Status))
       }
       return sdk.OK("")
   }
   ```

   The plugin decides every outcome: `ok`, `failed` (never works), `blocked` (a setting needs fixing) or `retry`.
   Girvi records it and does what it says.
3. **Test the logic** with `go test`, using `sdktest.New()` as the host and `sdktest.Event(t, sdk.EventLoanCreated,
   settings)` as the input.
4. **Test the build** with `girvi-plugin test`. It runs the real `.wasm` in Girvi's own runtime (package `sandbox`),
   so imports, limits and answers are handled exactly as Girvi does. Storage is in memory, and network requests are
   not made: they are answered from `--respond` and listed, so nothing is sent. The first line names the runtime
   version; Girvi shows the one it uses on its Plugins screen, and the same version behaves the same.

   ```sh
   girvi-plugin test --event payment.received --set url=https://example.com/hook --respond 503
   girvi-plugin test --kind test             # as the Send test button sends it
   girvi-plugin test --json                  # everything, for scripts
   ```

5. **Install** the zip from Girvi's Plugins screen and approve it.

### With an AI assistant

`girvi-plugin-mcp` is an MCP server with the same abilities: `read_contract`, `new_plugin`, `validate_plugin`,
`build_plugin` and `test_plugin`. Add it to a client, for example Claude Code:

```sh
claude mcp add girvi-plugins -- go run github.com/anant-barlota/girvi-plugins/cmd/girvi-plugin-mcp@<version>
```

Then ask for a plugin in plain words. The assistant reads the contract, makes the plugin, and builds and tests it
until it passes; tests never send anything over the network.

### Without a release

Until there is a tagged release, build the tools from a checkout and point new plugins at it:

```sh
git clone https://github.com/anant-barlota/girvi-plugins && cd girvi-plugins
go build -o ~/bin/ ./cmd/girvi-plugin ./cmd/girvi-plugin-mcp
girvi-plugin new ~/sms --id your-name/sms --sdk "$PWD"
```

For the MCP server, pass the checkout as `sdk` when calling `new_plugin`.

### Other languages

Any language that compiles to a WASI preview 1 reactor module with the exports and imports in the contract can
be used. Go is the only one with an SDK.

## Release it

Tag a version that matches `version` in `plugin.json`, and use the shared workflow:

```yaml
# .github/workflows/release.yml
on:
  push:
    tags: ['v*']
jobs:
  release:
    uses: anant-barlota/girvi-plugins/.github/workflows/release-plugin.yml@<commit>
```

It builds the bundle with the same SDK version your plugin uses and publishes the zip and its SHA-256 as a GitHub
Release. Then list it in the [plugin catalog](https://github.com/anant-barlota/girvi-registry), or give the zip to
whoever will install it.

## What Girvi does about a plugin you did not write

- **Approval is for one file.** A user sees what a plugin is told, given and can reach, and approves that exact
  bundle. Different code under the same id must be approved again, starts off everywhere, and gets none of the old
  code's secrets.
- **A sandbox, not trust.** No files, environment or database. Network only to the hosts in the manifest, checked
  against the address actually connected to, so a plugin cannot reach the computer or the shop's network unless the
  user typed that address. Memory, time, storage and wake-ups are capped.
- **Customer details on request only**, and shown to the user at approval.
- **Saving a loan never waits for a plugin.** Events are recorded with the change and delivered after it.

## License

MIT. See [LICENSE](LICENSE).
