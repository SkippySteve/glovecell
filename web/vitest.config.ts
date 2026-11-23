import {defineConfig} from 'vitest/config';

// Unit tests run in Node; widget tests (*.test.tsx) opt into jsdom with a
// `// @vitest-environment jsdom` first line and src/test/dom.ts.
// react-ui ships extensionless ESM imports that only a bundler resolves, so Vitest transforms it.
// testTimeout: the image build runs these tests beside other builds on one CI pod,
// where reading node_modules or loading a lazy chunk can take longer than Vitest's
// 5 s default. No test here measures speed, so a slow machine must not fail one.
export default defineConfig({
  test: {testTimeout: 30_000, include: ['src/**/*.test.ts', 'src/**/*.test.tsx'], environment: 'node', server: {deps: {inline: [/@openuidev\/react-ui/]}}},
});
