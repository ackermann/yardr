// Runs external-read.test.mts with the BDP clone's @bdp/* source aliases.
// Invoke from the BDP clone: pnpm exec vitest run --config <this file>.
const repo = process.env.BDP_REPO;
if (!repo) throw new Error("BDP_REPO must point at a built gastownhall/bdp clone");
const base = (await import(`${repo}/vitest.config.ts`)).default;

export default {
  root: import.meta.dirname,
  resolve: base.resolve,
  test: {
    globals: true,
    include: ["external-read.test.mts"],
    testTimeout: 600_000,
  },
};
