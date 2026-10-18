import {defineConfig} from 'vitest/config';

// react-ui ships extensionless ESM imports that only a bundler resolves, so Vitest transforms it.
export default defineConfig({
  test: {include: ['src/**/*.test.ts'], environment: 'node', server: {deps: {inline: [/@openuidev\/react-ui/]}}},
});
