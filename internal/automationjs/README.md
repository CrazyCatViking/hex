# Automation JavaScript engine

Hex embeds a minimal QuickJS-NG 0.17.0 WebAssembly reactor built from commit
`6d46d07d04041b40f4f49eaa7fdebe44c314c699`. The engine's MIT license is in
`engine/LICENSE`.
Notices for linked WASI runtime components are in `engine/THIRD_PARTY_NOTICES`.

With WASI SDK 27 the checked-in engine's SHA-256 is
`4753d6c29d2a1ddbdff2165515f2942b2f4cf1fc88a28e4f7e9d1144a664e53e`.

Normal Go builds embed the checked-in `engine.wasm`; they need no C compiler,
CGO, Node.js, runtime downloads, or server-side package installation.

To rebuild the engine, install [WASI SDK 27](https://github.com/WebAssembly/wasi-sdk/releases/tag/wasi-sdk-27) and run:

```sh
WASI_SDK_PATH=/path/to/wasi-sdk-27.0 go generate ./internal/automationjs
go test ./internal/automationjs ./server -run 'Script|Automation'
```

`generate.go` downloads that exact engine commit and compiles its four core C
files with `engine/bridge.c`. It does **not** link `quickjs-libc`, its OS or std
modules, native module loading, workers, or bytecode loading. The bridge rejects
imports and passes only bounded JSON through the `hex.invoke` host function.

Every execution gets a fresh anonymous Wasm module. Only compiled engine code is
shared. Wazero and the Wasm binary cap linear memory at 64 MiB; QuickJS caps its
heap at 32 MiB and stack at 1 MiB. Wazero's close-on-context-done instrumentation
terminates guest execution, including loops in engine built-ins. Five seconds of
computation are available per invocation; authorized Go operations pause that
timer but retain the parent run deadline. Four sandbox instances may execute at
once. A separate single-instance lane handles compile-only validation so owners
can deploy definitions while all execution slots are occupied.

WASI has no mounted filesystem, inherited environment, arguments, stdin, or
sockets. Its output is discarded. The Go bridge never shares reflected Go
objects or pointers with JavaScript. Host implementations must enforce
authorization, validate inputs, bound outputs and work, and honor cancellation.
