# Custom Hex server

This executable belongs in a consuming platform repository. It imports the Hex handler and the optional `server/dev` adapter, and adds a custom `/api/platform` endpoint. It does not use `cmd/hex-server`.

For a quick run from this checkout after building/linking the CLI:

```sh
hex dev examples/custom-server
```

Or copy `main.go` and `hex.dev.json` into your own Go module, add Hex as a dependency (using a local `replace` or workspace while developing), run `go mod tidy`, and execute `hex dev` from that repository.

You can also run `go run .` or the compiled binary directly; `dev.Open` requires no mode flag. This starts the API on `127.0.0.1:8081` by default. The CLI is optional orchestration for NGINX and matching paths/ports. Select service-backed local providers with `hex dev --services postgres,azurite` when deliberately testing those integrations.

The example explicitly chooses local defaults. A deployed platform should explicitly choose its production providers. Hex does not infer or manage development versus production mode.

See [Local development](../../docs/local-development.md) for setup, environment variables, optional services and tests.
