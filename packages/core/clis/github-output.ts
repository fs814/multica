import { z } from "zod";
import type { RuntimeCLIRunRequest } from "../types/cli";

const repositorySchema = z.object({
  repository: z.string().regex(/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/),
  description: z.string().catch(""),
  language: z.string().catch(""),
  stars: z.string().catch(""),
  gained: z.string().catch(""),
});
const trendingSchema = z.object({
  since: z.enum(["daily", "weekly", "monthly"]),
  repositories: z.array(repositorySchema),
});

export function parseGitHubTrendingOutput(run: RuntimeCLIRunRequest) {
  if (run.cli_key !== "gh-trending" || run.status !== "completed" || run.exit_code !== 0 || run.truncated || !run.output) return null;
  try {
    const result = trendingSchema.safeParse(JSON.parse(run.output));
    if (!result.success) return null;
    return {
      ...result.data,
      repositories: result.data.repositories.map((repository) => ({
        ...repository,
        // Derive links from validated repo names; never trust URLs in CLI output.
        url: `https://github.com/${repository.repository}`,
      })),
    };
  } catch {
    return null;
  }
}
