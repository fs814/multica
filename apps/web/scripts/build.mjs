import { spawnSync } from "node:child_process";
import process from "node:process";

// Next's production compiler assumes NODE_ENV is either unset or exactly
// "production". A developer-level root .env may intentionally carry another
// value for local tooling; do not let dotenv propagation turn a production
// build into a mixed React/Next mode (which can fail during prerender).
const env = { ...process.env, NODE_ENV: "production" };

function runPnpmExec(args) {
  const pnpmCli = process.env.npm_execpath;
  const command = pnpmCli ? process.execPath : process.platform === "win32" ? "pnpm.cmd" : "pnpm";
  const commandArgs = pnpmCli ? [pnpmCli, "exec", ...args] : ["exec", ...args];
  const result = spawnSync(command, commandArgs, {
    cwd: process.cwd(),
    env,
    stdio: "inherit",
  });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
}

runPnpmExec(["fumadocs-mdx"]);
runPnpmExec(["next", "build", "--webpack"]);
