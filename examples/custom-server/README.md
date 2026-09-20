# Custom Hex server

This executable belongs in a consuming platform repository. It imports the Hex handler and the optional `server/dev` adapter, and adds a custom `/api/platform` endpoint. It does not use `cmd/hex-server`.

For a quick run from this checkout after building/linking the CLI:

```sh
hex dev examples/custom-server
```

Or copy `main.go` and `hex.dev.json` into your own Go module, add Hex as a dependency (using a local `replace` or workspace while developing), run `go mod tidy`, and execute `hex dev` from that repository.

Select service-backed local providers with `hex dev --services postgres,azurite`. The example is development-only: `dev.Open` requires `HEX_DEV=1`, which the CLI supplies. In a deployable platform, select this configuration branch only when developing and build your own production providers otherwise.

See [Local development](../../docs/local-development.md) for setup, environment variables, optional services and tests.
