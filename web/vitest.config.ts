import {defineConfig} from 'vitest/config';

// Unit tests run in Node; widget tests (*.test.tsx) opt into jsdom with a
// `// @vitest-environment jsdom` first line and src/test/dom.ts.
// react-ui ships extensionless ESM imports that only a bundler resolves, so Vitest transforms it.
export default defineConfig({
  test: {include: ['src/**/*.test.ts', 'src/**/*.test.tsx'], environment: 'node', server: {deps: {inline: [/@openuidev\/react-ui/]}}},
});
