import { defineConfig } from 'vitest/config'

/**
 * Test config is kept out of vite.config.ts on purpose.
 *
 * The suite covers pure logic — nav partitioning and palette ranking — so it
 * needs neither the React nor the Tailwind plugin, and keeping the two configs
 * apart avoids Vite's and Vitest's bundled plugin types having to agree on a
 * version.
 */
export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})
