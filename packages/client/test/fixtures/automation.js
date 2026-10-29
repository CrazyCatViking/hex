// @ts-check

/** @param {import("@crazycatviking/hex/automations").AutomationContext} hex */
export default async function run(hex) {
  const documents = await hex.db.query("issues", { limit: 50 });
  const count = documents.filter(
    (document) => Number(document.data.count) >= 5,
  ).length;

  await hex.call("slack.post-message", { text: String(count) });
  await hex.action("archive", { count });
  await hex.db.save("reports", { count }, { id: hex.now.date });
  await hex.ai.complete({
    model: "general",
    prompt: "Summarize",
    tools: ["crm.*"],
  });
  hex.log("complete", { count });
  console.info("complete", { count });

  // @ts-expect-error Run metadata cannot be changed by a script.
  hex.run.dryRun = false;
  // @ts-expect-error Hex has no unrestricted HTTP capability.
  await hex.fetch("https://example.com");
  // @ts-expect-error Limits must be numbers.
  await hex.db.query("issues", { limit: "50" });
  // @ts-expect-error Operations cannot select a different site.
  await hex.db.query("issues", { site: "another-site" });
  // @ts-expect-error Endpoint names must include an integration.
  await hex.call("post-message");
  // @ts-expect-error All workflow state lives in JavaScript, not declared steps.
  void hex.steps;
  // Workflow input is supplied by the durable job.
  void hex.input;
  await hex.workflow.step("count", async () => count);
  await hex.workflow.start("child", { count }, { idempotencyKey: "child-1" });
  // @ts-expect-error Workflow keys are required for durable enqueueing.
  await hex.workflow.start("child", { count }, {});
  // @ts-expect-error Browsers cannot choose site execution authority.
  await hex.sites.call("inventory", "reserve", {}, { version: 1, as: "user" });
  return { count };
}

/** @type {string} */
const site = globalThis.hex.site;
void site;
