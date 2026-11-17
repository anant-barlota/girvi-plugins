# The girvi/v1 plugin contract

Everything a plugin author needs to know about how Girvi runs a plugin. It is
complete on its own: an author never needs to read Girvi's code.

## What a plugin is

A plugin is a **bundle**: a zip with two files at its root.

| File | What it is |
|---|---|
| `plugin.json` | The manifest: who the plugin is and what it may do. Read without running anything. |
| `plugin.wasm` | The code: a WebAssembly module (WASI preview 1, reactor). |

Girvi runs the module in a sandbox inside the app. It has **no** files, no
environment variables, no arguments and no direct network or database access.
Everything outside its own memory goes through the host functions below, and
only as far as `plugin.json` allows.

A user installs a bundle, reads what it may do, and approves it. Approval is
for that exact file. A plugin is then off for every firm until the user turns
it on for one. Different code later installed under the same id must be
approved again, starts off for every firm, and is given none of the old
code's secrets.

## plugin.json

```json
{
  "id": "your-name/webhook",
  "name": "Webhook",
  "version": "1.0.0",
  "api": "girvi/v1",
  "description": "Posts loan events to a URL you choose.",
  "author": "Your Name",
  "events": ["loan.created", "payment.received", "loan.redeemed"],
  "fields": ["customer.name", "customer.phone"],
  "http_hosts": ["{setting:url}"],
  "settings": [
    {"key": "url", "title": "Webhook URL", "required": true},
    {"key": "token", "title": "Token", "secret": true},
    {"key": "verbose", "title": "Verbose", "type": "boolean", "default": "false", "advanced": true}
  ],
  "delivery": "at_least_once"
}
```

| Field | Required | Meaning |
|---|---|---|
| `id` | yes | `publisher/name`, lower case letters, digits and `-`. `girvi/` is reserved for official plugins. |
| `name`, `version` | yes | Shown to the user. |
| `api` | yes | Must be `"girvi/v1"`. |
| `description`, `author` | no | Shown to the user. |
| `events` | no | Events the plugin is told about: `loan.created`, `payment.received`, `loan.redeemed`. |
| `fields` | no | Personal details the plugin is given: `customer.name`, `customer.phone`. Without them those fields are absent from every event. |
| `http_hosts` | no | Where the plugin may send requests. See below. Without it, the plugin has no network at all. |
| `settings` | no | The settings form Girvi shows, per firm. See below. |
| `delivery` | no | `at_least_once` (default) or `at_most_once`. See Deliveries. |

Unknown fields are refused: Girvi will not silently ignore a grant it does not
understand. The file may be at most 64 KB.

### http_hosts

Each entry is one of:

| Entry | Allows |
|---|---|
| `"api.example.com"` | That host, on any port. |
| `"api.example.com:8443"` | That host, on that port only. |
| `"*.example.com"` | Any subdomain of `example.com`, not `example.com` itself. At least two labels after `*.` (`*.com` is refused). |
| `"localhost:9000"`, `"192.168.1.20:8080"` | A host on this computer or the shop's network. Must name a port. |
| `"{setting:KEY}"` | The exact host and port of the URL the user typed into setting `KEY`. That setting cannot have a default. |

Girvi checks the address a request actually connects to. A name granted as a
public host that resolves to this computer or a private network is refused, so
a plugin cannot reach Girvi itself or other machines in the shop unless the
user typed that address or the manifest names it. Redirects are not followed.

### settings

| Field | Meaning |
|---|---|
| `key` | `a-z`, digits and `_`, starting with a letter. Unique. |
| `title`, `description` | Shown in the form. |
| `type` | `string` (default) or `boolean` (the value is `"true"` or `"false"`). |
| `secret` | Never shown again after it is saved, only "saved". |
| `required` | Marked as required in the form. Girvi does not stop a call when it is empty: the plugin should return `blocked` with a note. |
| `default` | Given to the plugin when the user has not set a value. |
| `advanced` | Hidden behind "Advanced". |

Settings are per firm. A plugin is given only the keys its manifest declares.

## The code

### Exports (Girvi calls these)

| Export | Called for |
|---|---|
| `on_event` | Required. Every event the plugin subscribes to, and Send test. |
| `on_wake` | A wake-up the plugin asked for with `wake_at`. Needed only if it calls `wake_at`. |

Each call runs in a **fresh instance** that is thrown away afterwards: nothing
in memory survives from one call to the next. Use storage for that. The
module's `_initialize` runs first on every call.

### Input

The plugin reads its input with `input_len` and `input_read`. It is JSON:

```json
{
  "kind": "event",
  "delivery_id": "01a1e953-e8db-73d0-8597-f33ce2ae524d",
  "attempt": 1,
  "event": { "id": "...", "type": "loan.created", "version": 1, "occurred_at": "...", "data": { } },
  "wake": "",
  "firm": { "id": "bk-moolchand", "name": "BK Moolchand & Co." },
  "settings": { "url": "https://example.com/hook" }
}
```

| Field | Meaning |
|---|---|
| `kind` | `event`, `test` (Send test, with a made-up loan), or `wake`. |
| `delivery_id` | Unique per plugin and event, the same on every retry. Use it as an idempotency key where the receiver supports one. `event.id` is the same value. |
| `attempt` | 1 on the first call. |
| `event` | For `event` and `test`. See Events. |
| `wake` | For `wake`: the payload passed to `wake_at`. |
| `firm` | The shop. Settings, storage and wake-ups are all per firm. |
| `settings` | This firm's values, with defaults applied. |

### Outcome

The plugin ends every call by writing one JSON object with `output`:

```json
{ "kind": "retry", "note": "the receiver is busy (HTTP 503)", "retry_in_seconds": 300 }
```

| `kind` | Meaning | What Girvi does |
|---|---|---|
| `ok` | Done. | Records it. Drops the event's contents. |
| `failed` | This item will never work. | Records it. The user can Send again, once, within 7 days. |
| `blocked` | The shop must fix a setting first. | Holds it, and calls again after the plugin's settings change. |
| `retry` | Try again later. | Calls again after `retry_in_seconds` (at least 1, at most a year). |

`note` is shown to the user in the plugin's activity. Do not put secrets in
it. The plugin decides every outcome; Girvi does not retry on its own
judgement, with one exception: a call that ends with **no answer**.

### No answer

A call has no answer when the plugin panics, exits, runs longer than 30
seconds, uses more than 64 MB of memory, passes a bad pointer, or writes no
outcome. Girvi cannot know whether the plugin's side effect (a message sent)
happened, so the manifest's `delivery` decides:

- `at_least_once`: called again after 1, 2, 3, 4 minutes; failed after 5
  calls in a row with no answer. A duplicate is possible.
- `at_most_once`: failed at once, for the user to Send again. A loss is
  possible, a duplicate is not.

The same applies when the app closes during a call.

### Host functions (import module `girvi_v1`)

Pointers and lengths are `i32` offsets into the plugin's own memory. A pointer
outside it ends the call with no answer.

| Function | Signature | Meaning |
|---|---|---|
| `input_len` | `() -> u32` | Length of the input. |
| `input_read` | `(ptr)` | Copies the input to `ptr`. |
| `output` | `(ptr, len)` | Sets the outcome. Call once, at the end. |
| `log` | `(ptr, len)` | A line in this call's log, shown in the activity. At most 50 lines of 500 bytes are kept. |
| `http_request` | `(ptr, len) -> u32` | Makes a request; returns the length of the response, which `result_read` copies out. Needs `http_hosts`. |
| `result_read` | `(ptr)` | Copies the last result of `http_request` or `storage_get`. |
| `storage_get` | `(key_ptr, key_len) -> i32` | Length of the value, `-1` if there is none, `-2` on error. Copy it with `result_read`. |
| `storage_set` | `(key_ptr, key_len, val_ptr, val_len) -> i32` | `0`, or `-2` if refused. |
| `storage_delete` | `(key_ptr, key_len) -> i32` | `0`, or `-2`. |
| `wake_at` | `(unix_seconds i64, payload_ptr, payload_len) -> i32` | Asks for `on_wake` at a time, with a payload. `0`, or `-2` if refused. |

A module that imports anything else, or imports `http_request` without
`http_hosts` in its manifest, is refused at install. A Go plugin also imports
`wasi_snapshot_preview1` for its clock, random numbers and stderr; that is
allowed, with no files, arguments or environment.

The HTTP request and response are JSON. `body` is base64, as Go's
`encoding/json` writes a `[]byte`:

```json
{ "method": "POST", "url": "https://example.com/hook", "headers": { "Content-Type": "application/json" }, "body": "eyJ...In0=" }
{ "status": 204, "body": "", "error": "" }
```

`error` is set, and `status` is `0`, when the request was not made or got no
answer: not allowed, could not connect, timed out. Responses over 1 MB are
cut.

### Limits

| What | Limit |
|---|---|
| Memory per call | 64 MB |
| Time per call | 30 seconds |
| `plugin.wasm` | 64 MB |
| `plugin.json` | 64 KB |
| HTTP response body | 1 MB |
| Storage key | 256 bytes |
| Storage value | 1 MB |
| Storage per plugin per firm | 1000 keys, 16 MB |
| Wake-ups waiting per plugin per firm | 100 |
| Soonest wake-up | 1 second from now |
| Latest wake-up or retry | 1 year from now |
| Wake payload | 64 KB |
| stdout and stderr kept | 16 KB (the first line is shown when a call has no answer) |

## Deliveries

- An event is recorded in the same transaction as the loan or payment that
  caused it, for every plugin that is on for the firm and subscribed. Nothing
  is recorded for a plugin that is off, and nothing is replayed when it is
  turned on.
- Girvi calls the plugin after the change is saved, in the background. A
  plugin can never stop or slow down saving a loan or payment.
- Each plugin is called for one item at a time per firm, oldest first.
  Plugins run side by side.
- An item waiting for a retry, or blocked, does **not** hold up the items after
  it. A plugin can see a later event before an earlier one it asked to retry.
  Use `occurred_at` when order matters.
- An event still waiting after 7 days is failed, not sent late.
- When a loan is cancelled, its events that have not been delivered are
  failed and are never sent.
- Everything above is metadata only: once an item is done, Girvi keeps who,
  what and when, never the contents.

## Events

Every event has the same envelope:

| Field | Meaning |
|---|---|
| `id` | The delivery id. |
| `type` | `loan.created`, `payment.received` or `loan.redeemed`. |
| `version` | `1`. Adding a field does not change it; removing or renaming one would. |
| `occurred_at` | When it happened, RFC 3339 UTC. |
| `data` | Depends on the type. |

Values:

- **Money** is `{ "paise": 150000, "display": "₹1,500.00" }`. Integer paise; the
  display string is formatted the way Girvi's printed documents are. Never
  compute or format money yourself.
- **Weight** is `{ "milligrams": 10500, "display": "10.500 g" }`.
- **Date** is `{ "iso": "2026-11-12", "display": "12 Nov 2026" }`.
- **Rate** is `{ "basis_points": 1400, "per": "year", "display": "14% p.a." }`,
  absent when the loan has no rate.
- **Customer** is `{ "id", "name", "phone", "phone_e164" }`. `name` needs
  `customer.name` in `fields`; `phone` (as stored) and `phone_e164`
  (`+91…`, only for a usable Indian mobile) need `customer.phone`.

| Type | When | `data` |
|---|---|---|
| `loan.created` | A Form F is saved. | `customer`, `loan` (id, number, pledged_on, principal, value, interest_rate, total_net_weight, total_gross_weight, total_pieces, status) |
| `payment.received` | Every Form H, including the one that closes the loan. | `customer`, `loan` (id, number), `payment` (id, number), `amount`, `principal`, `interest`, `date`, `outstanding_principal`, `loan_closed` |
| `loan.redeemed` | After the closing `payment.received`. | `customer`, `loan` (id, number), `redeemed_on`, `returned_weight` |

A plugin subscribed to both `payment.received` and `loan.redeemed` gets two
events for a closing payment and should act on one.

Samples of each are in `contract/fixtures`, or from `girvi-plugin contract
--event <type>`.

## Writing one

With the Go SDK (`github.com/anant-barlota/girvi-plugins/sdk`):

```
girvi-plugin new ./my-plugin --id your-name/my-plugin
cd my-plugin
go test ./...          # handler logic, natively, with sdktest
girvi-plugin build     # plugin.wasm and dist/<name>-<version>.zip
girvi-plugin test --event loan.created --set url=https://example.com/hook
```

Any language that compiles to a WASI preview 1 reactor module with the
exports and imports above can be used; Go is the only one with an SDK today.
