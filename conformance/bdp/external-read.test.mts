/**
 * Run BDP Read conformance (packaged/black-box rows) against an already-running server.
 * Adapted from runPackagedTarget in BDP's scripts/generate-read-cohort.mts, which
 * launches BDP's own two servers per scenario; see README.md in this directory.
 *
 * Env:
 *   BDP_TARGET_SCOPE   canonical Scope URL the server emits (required), e.g. http://127.0.0.1:8899/scope/
 *   BDP_TARGET_DIAL    host:port to dial (default: host/port of BDP_TARGET_SCOPE; plain HTTP only)
 *   BDP_FIXTURE        fixture JSON path (default: clone's read-reference-v1.json)
 *   BDP_CAPABILITIES   optional comma list restricting fixture capabilities the target claims
 *   BDP_REPORT         optional path to write the full JSON report
 */
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import {
  createConformanceArtifactBundle, createJsonSchemaValidator, createRawHttpScenarioTarget,
  deriveReadCohortNotApplicableRows, deriveReadCohortRequiredScenarioIds,
  deriveReadCohortSelfCertifiableIds, runConformanceMatrix,
} from "@bdp/conformance";
import {
  controlledReadAdvertisedLimitsCapability, controlledReadCapability,
  controlledReadProblemCapability, controlledReadScopeRestoreCapability,
  createCurrentReadWireValidator, startControlledTypeDescriptorPublisher, successorDescriptorBodies,
} from "@bdp/conformance/testing";
import { createBdpClientScenarioActionExecutor } from "@bdp/client/testing";

// The BDP clone (gastownhall/bdp, built with pnpm install && pnpm build).
const repo = process.env.BDP_REPO ?? "";
if (!repo) throw new Error("BDP_REPO must point at a built gastownhall/bdp clone");
const bytes = (p: string) => readFileSync(path.isAbsolute(p) ? p : path.join(repo, p));
// Lifecycle-only capabilities need the in-process controlled composition (x-bdp-conformance-* headers).
const IN_PROCESS_ONLY = new Set([controlledReadCapability, controlledReadAdvertisedLimitsCapability,
  controlledReadProblemCapability, controlledReadScopeRestoreCapability]);
// Needs a per-launch BDP_SERVER_INTERNAL_FAULT_RESOURCE fault injection; not available on a live URL.
const LAUNCH_ONLY = new Set(["unexpected-internal-fault"]);

test("external Read conformance", async () => {
  const scope = process.env.BDP_TARGET_SCOPE;
  if (!scope) throw new Error("BDP_TARGET_SCOPE is required");
  const scopeUrl = new URL(scope);
  const [host, port] = (process.env.BDP_TARGET_DIAL ?? `${scopeUrl.hostname}:${scopeUrl.port || 80}`).split(":");
  const fixturePath = process.env.BDP_FIXTURE ?? "packages/conformance/fixtures/read-reference-v1.json";
  const bundle = createConformanceArtifactBundle({
    catalog: { bytes: bytes("packages/conformance/catalog/read-v1.json"), label: "catalog/read-v1.json" },
    manifest: { bytes: bytes("packages/conformance/matrices/read-v1.json"), label: "matrices/read-v1.json" },
    fixture: { bytes: bytes(fixturePath), label: fixturePath },
  });
  const fixture = bundle.fixture as any;
  const restrict = process.env.BDP_CAPABILITIES?.split(",").map((s) => s.trim()).filter(Boolean);
  const capabilities = (fixture.capabilities as string[]).filter(
    (c) => !IN_PROCESS_ONLY.has(c) && !LAUNCH_ONLY.has(c) && (!restrict || restrict.includes(c)));

  const required = deriveReadCohortRequiredScenarioIds(bundle.catalog);
  const selfCert = new Set(deriveReadCohortSelfCertifiableIds(bundle.manifest, required));
  const notApplicable = new Set(deriveReadCohortNotApplicableRows(bundle.manifest, required, capabilities)
    .map((r: any) => r.scenarioId));
  const plans = new Map(bundle.manifest.scenarios.map((s) => [s.id, s]));
  const unrunnable = required.filter((id) => !selfCert.has(id) && !notApplicable.has(id) &&
    plans.get(id)!.setup.requires.some((c) => !capabilities.includes(c)));
  const selection = required.filter((id) => !selfCert.has(id) && !notApplicable.has(id) && !unrunnable.includes(id));

  const publisher = await startControlledTypeDescriptorPublisher([
    ...(fixture.typeDescriptors ?? []), ...successorDescriptorBodies(fixture)]);
  const target = createRawHttpScenarioTarget(async () => ({
    capabilities, bindings: fixture.bindings,
    dialRoute: { transport: "plain" as const, host, port: Number(port) },
    close: async () => {}, // server is externally managed; nothing to tear down
  }));
  // As BDP's packaged lane: the client actions refuse a record that is not
  // the current schema's before the client can paper over it.
  const schemaValidator = createJsonSchemaValidator(JSON.parse(bytes("schemas/bdp-v0.schema.json").toString()));
  try {
    const result = await runConformanceMatrix({
      scope, profile: "read", seed: 0, artifactBundle: bundle,
      execute: target.execute,
      actionExecutor: createBdpClientScenarioActionExecutor({
        validateReadWire: createCurrentReadWireValidator(schemaValidator),
        fetchImplementation: target.fetch, externalTypeDescriptorFetchImplementation: publisher.fetch }),
      harness: target.harness,
      schemaValidator,
      scenarioSelection: selection,
      declaredTargetLabel: "external-target",
    });
    if (process.env.BDP_REPORT) writeFileSync(process.env.BDP_REPORT, JSON.stringify(result, null, 2));
    for (const s of result.scenarios) {
      const failed = [...s.exchanges.flatMap((e: any) => e.assertions), ...(s.actions ?? []).flatMap((a: any) => a.assertions)]
        .filter((a: any) => !a.passed).map((a: any) => `${a.id}${a.message ? `: ${a.message}` : ""}`);
      console.log(`${s.state.padEnd(16)} ${s.id}${s.reason ? `  (${s.reason})` : ""}${failed.length ? `\n    ${failed.join("\n    ")}` : ""}`);
    }
    console.log(`skipped self-certified (lifecycle, in-process only): ${[...selfCert].join(", ")}`);
    console.log(`not-applicable (capability-gated): ${[...notApplicable].join(", ")}`);
    console.log(`unrunnable here (setup capability unavailable on a live URL): ${unrunnable.join(", ")}`);
    const counts: Record<string, number> = {};
    for (const s of result.scenarios) counts[s.state] = (counts[s.state] ?? 0) + 1;
    console.log("SUMMARY", JSON.stringify(counts));
    expect(result.scenarios.filter((s) => s.state !== "pass").map((s) => s.id)).toEqual([]);
  } finally {
    await target.close();
    await publisher.close();
  }
});
