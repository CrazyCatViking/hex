# Durable workflow composition

Workflows run the existing automation JavaScript sandbox with persisted input,
checkpoints and wake times. Start a deployed automation through
`hex.workflows.start(name, input, {idempotencyKey})` in a browser, or
`hex.workflow.start(...)` in an automation. An optional automation `inputSchema` JSON
Schema validates workflow input. Grant `kind: "workflow"`, `resource: <name>`,
`operations: ["run"]` to allowed users or `automation:<name>` callers.

```js
export default async function (hex) {
  const reservation = await hex.workflow.step("reserve", () =>
    hex.sites.call("inventory", "reserve-stock", hex.input, { version: 1 })
  );

  const children = [];
  for (const region of ["north", "south"]) {
    children.push(await hex.workflow.start("notify-region", {
      region, reservation,
    }, { idempotencyKey: `${hex.workflowId}-${region}` }));
  }
  for (const child of children) await hex.workflow.wait(child.id);

  await hex.workflow.sleep("follow-up-delay", 60_000);
  return { reservation, notified: children.length };
}
```

## Replay model

- Each job pins its deployed script revision and input; retries do not silently
  adopt new code. Removing or disabling its automation stops subsequent attempts.
- `hex.input` and `hex.workflowId` are frozen runtime metadata.
- Scripts restart from the beginning on wake/retry. A completed named
  `hex.workflow.step(name, callback)` returns its stored output and skips the
  callback. Use stable unique names, including IDs for loop iterations.
- Steps are sequential, not nested. JavaScript branching, loops, try/catch and
  separate child workflows provide composition. Independent child jobs can run
  on different workers; `Promise.all` within one script remains sequential host
  execution, as with other automations.
- `sleep(name, milliseconds)` stores a wake time (1 ms–30 days) and releases the
  worker. `wait(childId)` also releases the worker until the child completes.
  Suspension ends the attempt; catching its exception cannot perform more host
  operations. Waiting does not count as a failed retry.
- A step can commit an external effect before its checkpoint is saved. Such an
  interrupted step executes again. Cross-site calls inside a step automatically
  receive stable per-step/call-position keys when none are supplied. Other
  effects need their own idempotency, e.g. deterministic document IDs. Do not
  automatically retry non-idempotent third-party writes.
- Keep branching deterministic from input and checkpoint results. Wrap reads
  that affect later control flow in steps. Do not use changing clocks or random
  values to decide which effects replay.

Step outputs are limited to 16 KiB, 100 steps per workflow, and 1 MiB total job
state. Existing per-attempt sandbox/host limits apply. Workflow results use the
automation history's bounded output representation. Use stored documents for
large outputs. Child cycles are not resolved automatically; use acyclic graphs.

## Authority and operations

Starting a job authorizes enqueueing; its effects always use the automation's
current grants, never the initiating user's authority. Automation initiators'
workflow grants are also rechecked between attempts. Jobs use replica-safe
leases and fenced checkpoint/completion updates. Cancellation prevents new host
operations but cannot undo an already-running external request.

Failures retry with exponential backoff up to eight failures, then become
`dead`. A site owner can call `hex.workflows.retry(id)` after correcting the
problem; successful checkpoints are retained. `hex.workflows.cancel(id)` stops
a pending/running job. `hex.workflows.get(id)` returns status, run ID, attempts,
failures, output, error and checkpoints to callers with the workflow grant.
Owners can enumerate jobs with `hex.workflows.list(after)`, returning
`{items,after}` pages of 25.

HTTP: `POST /api/sites/<site>/workflows` with
`{name,input,idempotencyKey}`, `GET workflows/<id>`,
`POST workflows/<id>/retry`, `POST workflows/<id>/cancel`.

To compose events with workflows, an event handler starts a workflow using
`hex.event.id` as its idempotency key and passes `hex.event.data` as input. Event
delivery is acknowledged after durable enqueue; the independent workflow can
then sleep, fan out and resume without holding an event delivery lease.
