# Agent access and app actions

Local agents can use the CLI's saved sign-in to read protected content and perform operations explicitly exposed by an app backend. All commands support `--platform <profile>` and use the normal project/default profile otherwise. Run `hex login` in a terminal when a saved sign-in is unavailable; agents need neither browser cookies nor tokens in their input.

## Read protected content

```sh
hex fetch https://reports.hex.company.example/
hex fetch --site reports --path /help/
hex fetch --site reports --path '/api/custom/summary?month=2026-10'
hex fetch --site reports --path /assets/config.json --output config.json
```

`fetch` makes an authenticated GET and writes the original response body to stdout. It supports HTML, JSON, text and binary content; it does not run JavaScript. `--site` derives the hostname from `siteBaseURL` (falling back to the configured server), and `--path` defaults to `/`. A full URL cannot be combined with `--site` or `--path`.

URLs must belong to the configured server origin, parent site origin, or a direct site subdomain with the configured scheme and port. HTTPS is required except on loopback. Redirects are followed only within the original origin, so credentials are never forwarded to another host. Authentication and site/path authorization still happen at the hosting gateway and Hex API.

`--output` streams through a temporary file and replaces the destination only after a successful download. The output directory must already exist. Without it, the body is streamed directly, with no added JSON envelope or newline. Non-success HTTP statuses fail the command.

## Read application records and files

```sh
hex data list --site reports --collection monthly-reports --limit 100
hex data list --site reports --collection monthly-reports --after report-123
hex data get --site reports --collection monthly-reports --id report-123

hex files list --site reports --prefix exports/
hex files get --site reports --key exports/latest.csv --output latest.csv
```

Data and file listings return JSON. Documents retain the existing `{id, data, createdBy?}` format. Lists are pages of 1–100 documents ordered by ID; continue with the last ID in `--after` until an empty page. Collection and document identifiers follow the existing 1–64 character API rules. File listing filters readable keys by the literal `--prefix`; file downloads return bytes to stdout or `--output`.

These are read-only helpers over existing APIs and work on platforms without action support. The API applies existing collection rules, creator-only reads and file-prefix rules. Changes through this CLI interface go through app actions.

## Discover and execute actions

```sh
hex actions list --site expenses
hex actions describe --site expenses create-draft
hex actions run --site expenses create-draft --input @expense.json
```

`list` returns a JSON array of the actions available to the caller, sorted by name. `describe` returns one contract:

```json
{
  "name": "create-draft",
  "description": "Create an expense draft for the current user.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "amount": { "type": "number", "exclusiveMinimum": 0 },
      "currency": { "type": "string", "enum": ["NOK", "EUR"] }
    },
    "required": ["amount", "currency"],
    "additionalProperties": false
  },
  "outputSchema": {
    "type": "object",
    "properties": { "id": { "type": "string" } },
    "required": ["id"],
    "additionalProperties": false
  }
}
```

`run` reads one JSON value from `@<file>` (paths resolve relative to the working directory), fetches the action's current contract, and validates both the contract and input before sending an execution request. It also validates the returned output. Invalid input never reaches execution. Structured results go to stdout, diagnostics to stderr, and errors exit nonzero.

Input is limited to 1 MiB; each schema is limited to 64 KiB. Schemas default to JSON Schema draft 2020-12. Standard constraints, nested objects, arrays, composition, local `$ref`/`$defs`, and format assertions are supported. Schemas must be self-contained: the CLI and server do not download external schema resources. Validation does not coerce types, insert defaults or remove fields. Use `additionalProperties: false` when extra fields should be rejected.

Execution repeats authorization and input validation at the backend, so direct HTTP callers receive the same checks. Handler results must fulfill the output contract. Actions are not automatically retried: a handler may have made changes before a response is lost or output validation fails. Output validation does not roll back those changes; implement transactions or idempotency within an action when its workflow needs them.

## Register operations in a consuming server

An app backend registers its contract and handler together in `hex.ActionRegistry` and assigns the registry to `hex.Config.Actions` before calling `hex.New`. The zero-value registry is ready to use. `Register` returns an error for invalid schemas, invalid names, missing descriptions/handlers, or duplicate names within a site.

```go
actions := new(hex.ActionRegistry)
err := actions.Register("my-app", hex.Action{
    Definition: hex.ActionDefinition{
        Name:        "create-task",
        Description: "Create a task for the current user.",
        InputSchema: json.RawMessage(`{
            "type":"object",
            "properties":{"title":{"type":"string","minLength":1}},
            "required":["title"],
            "additionalProperties":false
        }`),
        OutputSchema: json.RawMessage(`{
            "type":"object",
            "properties":{"id":{"type":"string"}},
            "required":["id"],
            "additionalProperties":false
        }`),
    },
    Handler: func(ctx context.Context, caller hex.ActionContext, input json.RawMessage) (any, error) {
        options, err := caller.CollectionWriteOptions("tasks")
        if err != nil {
            return nil, err
        }
        document, err := database.Put(ctx, caller.Site, "tasks", rand.Text(), input, options)
        if err != nil {
            return nil, err
        }
        return map[string]string{"id": document.ID}, nil
    },
})
if err != nil {
    return err
}
config.Actions = actions
handler := hex.New(config)
```

This example uses `context`, `crypto/rand`, `encoding/json`, the Hex package and an existing database provider. A complete executable is in [examples/custom-server](../examples/custom-server/README.md).

Actions run as the existing user. They require site access and default to the site's editor audience. Set `Action.Audience` to existing viewers/owners or principals when an operation needs a different audience; there are no separate agent scopes or sessions. Owners pass audience rules as elsewhere in Hex. Without an identity/access configuration, the platform's existing open-access behavior applies; with an identity resolver configured, unresolved identities cannot use action routes.

Handlers receive `ActionContext.Site`, `Identity` and `Role`. When accessing providers directly, use `CollectionWriteOptions` to enforce collection write rules and creator tracking, `CanReadDocument` for collection reads, and `CanReadFile`/`CanWriteFile` for file rules. Custom business rules and external-service authorization belong in the handler. Raw providers do not independently enforce the site's HTTP access policy.

Return `hex.ErrForbidden` for denied operations, `hex.ErrNotFound` for missing resources, or `&hex.ActionError{Message: "..."}` for a user-correctable business-rule failure. Other errors are logged and produce a generic internal-server error.

Static publication does not register handlers or execute a manifest. A consuming backend implements actions; the reference server has none unless extended. Discovery advertises `capabilities.actions` when a registry is configured.

### HTTP contract

| Method | Route                                | Result                       |
| ------ | ------------------------------------ | ---------------------------- |
| GET    | `/api/sites/{site}/actions`          | Available action definitions |
| GET    | `/api/sites/{site}/actions/{action}` | One definition               |
| POST   | `/api/sites/{site}/actions/{action}` | Validated action output      |

POST takes the input JSON directly, not an envelope, and requires `X-Hex-Request: 1` like other Hex writes. Execution stays on these fixed endpoints; definitions contain no arbitrary execution URLs. Unknown actions return 404, denied operations 403, invalid input/business-rule failures 400, and unexpected handler/output failures 500. Platforms without an action registry do not register these routes.
