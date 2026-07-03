# qwencode-proxy

Local HTTP proxy between `qwen-code` and its model. Rewrites chat traffic via pluggable rules. Single Go binary, no dependencies.

## Install

Requires [qwen-code](https://github.com/QwenLM/qwen-code) (`npm install -g @qwen-code/qwen-code`).

Download a prebuilt binary from [Releases](https://github.com/anh-ld/qwencode-proxy/releases) — no Go needed. Extract and put it on your `PATH`.

Or, with Go 1.24+:

```bash
go install github.com/anh-ld/qwencode-proxy@latest
```

## Use

```bash
qwencode-proxy                # first run: setup, then launch qwen
qwencode-proxy "fix auth.go"  # any qwen args
qwencode-proxy off            # restore original settings.json
qwencode-proxy config [-e]    # show / edit config
```

### Tips

- Auto-detects upstream from `~/.qwen/settings.json`, backs it up, points `qwen` at `127.0.0.1:8788`.
- Always launch via `qwencode-proxy`. Once set up, `qwen` alone points at the proxy port, so it errors with `fetch failed` unless the proxy is running (or restore with `qwencode-proxy off`).
- Change port in `~/.config/qwencode-proxy/config.json`.
- Optional alias: `alias qwen=qwencode-proxy`.
- Port busy → change `port` in config.
- Config corrupt → run `qwencode-proxy setup`.

## Rules

Config (`~/.config/qwencode-proxy/config.json`):

```json
{
  "upstream": "https://dashscope.aliyuncs.com/compatible-mode/v1",
  "port": 8788,
  "rules": [
    { "type": "strip-pair",    "open": "<think>", "close": "</think>" },
    { "type": "set-param",     "params": { "model": "qwen-coder-plus" } },
    { "type": "inject-system", "position": "append", "text": "Be concise." },
    { "type": "replace",       "find": "foo", "replace": "bar" }
  ]
}
```

All rules accept `"enabled": true|false` (default `true`). Unknown types skipped. Streaming-safe: partial tags buffer; trailing tail flushes on `finish_reason`.

| Type             | Effect                              | Fields                                                                       |
| ---------------- | ----------------------------------- | ---------------------------------------------------------------------------- |
| `strip-pair`     | Drop `<open>…</close>` from response | `open` (default `<think>`), `close` (default `</mm:think>`)                    |
| `replace`        | Literal find/replace on response    | `find` (required), `replace` (default `""`)                                  |
| `inject-system`  | Add a system message to request     | `text` (required), `position` (`"prepend"` default / `"append"`)             |
| `set-param`      | Set fields on request body          | `params` (required, key/value object)                                        |

## Build & test

```bash
go build .
go test ./...
```
