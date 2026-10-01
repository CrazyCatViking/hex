# Custom Hex server

This executable belongs in a consuming platform repository. It imports the Hex handler and the optional `server/dev` adapter, adds a custom `/api/platform` endpoint, and registers a contract-validated `create-task` action for `my-app` when the database is enabled. It does not use `cmd/hex-server`.

For a quick run from this checkout after building/linking the CLI:

```sh
hex dev examples/custom-server
```

Or copy `main.go`, `actions.go`, and `hex.dev.json` into your own Go module, add Hex as a dependency (using a local `replace` or workspace while developing), run `go mod tidy`, and execute `hex dev` from that repository.

You can also run `go run .` or the compiled binary directly; `dev.Open` requires no mode flag. This starts the API on `127.0.0.1:8081` by default. The CLI is optional orchestration for NGINX and matching paths/ports. Select service-backed local providers with `hex dev --services postgres,azurite` when deliberately testing those integrations.

The example explicitly chooses local defaults. A deployed platform should explicitly choose its production providers. Hex does not infer or manage development versus production mode.

See [Local development](../../docs/local-development.md) for setup, environment variables, optional services and tests.

## Use the app through the CLI

After setting up the local profile, save `{"title":"Created through the CLI"}` in `task.json`, then run:

```sh
hex actions list --site my-app
hex actions describe --site my-app create-task
hex actions run --site my-app create-task --input @task.json
hex data list --site my-app --collection tasks
```

The handler uses `ActionContext.CollectionWriteOptions` to respect the collection's write rule and record the current user as the creator. Change the registered site name to match your app. Actions are registered by the backend and do not require a published frontend. See [Agent access and actions](../../docs/agents.md).
