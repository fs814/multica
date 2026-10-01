// Metro bundler configuration for the mobile app inside the multica monorepo.
// Watches the entire monorepo so type-only imports from packages/core/types/*
// resolve, looks up node_modules from both project and monorepo root, and
// uses Expo's built-in symlink support for pnpm's transitive deps.
// Hierarchical lookup is left enabled (default) — pnpm needs it.

const { getDefaultConfig } = require("expo/metro-config");
const { withNativeWind } = require("nativewind/metro");
const path = require("path");

const projectRoot = __dirname;
const monorepoRoot = path.resolve(projectRoot, "../..");

const config = getDefaultConfig(projectRoot);

config.watchFolders = [monorepoRoot];
config.resolver.nodeModulesPaths = [
  path.resolve(projectRoot, "node_modules"),
  path.resolve(monorepoRoot, "node_modules"),
];

module.exports = withNativeWind(config, { input: "./global.css", inlineRem: 16 });
