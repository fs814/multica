import { z } from "zod";
import type { RuntimeCLIRunRequest } from "../types/cli";

const optionalText = z.string().optional().catch(undefined);
const optionalBoolean = z.boolean().optional().catch(undefined);
const optionalCount = z.number().int().nonnegative().optional().catch(undefined);

const searchSchema = z.object({
  Code: z.literal(0),
  Data: z.object({
    HasMore: optionalBoolean,
    Items: z.array(z.object({
      Title: z.string().trim().min(1),
      Url: optionalText,
      ContentText: optionalText,
      ContentType: optionalText,
      AuthorName: optionalText,
      VoteUpCount: optionalCount,
      CommentCount: optionalCount,
    })),
  }),
});

const versionSchema = z.object({
  current_version: optionalText,
  latest_version: optionalText,
  update_available: optionalBoolean,
});
const hotSchema = z.object({
  Code: z.literal(0),
  Data: z.object({
    Items: z.array(z.object({
      Title: z.string().trim().min(1),
      Url: optionalText,
      Summary: optionalText,
    })),
  }),
});
const statusSchema = z.object({
  ok: z.boolean(),
  installed: z.boolean(),
  next_action: optionalText,
  auth: z.object({ configured: z.boolean(), source: optionalText }),
  cli: versionSchema.extend({ compatible: optionalBoolean }),
  skill: versionSchema.optional().catch(undefined),
  update_check: z.object({ status: optionalText }).optional().catch(undefined),
});

export interface ZhihuSearchItem {
  title: string;
  url: string | null;
  text: string;
  contentType?: string;
  author?: string;
  votes?: number;
  comments?: number;
}

export type ZhihuOutput =
  | { kind: "search"; items: ZhihuSearchItem[]; hasMore: boolean }
  | { kind: "hot"; items: ZhihuSearchItem[] }
  | { kind: "status"; data: z.infer<typeof statusSchema> }
  | { kind: "error"; code: number; message: string };

// CLI output is untrusted text. Only web links may become clickable; contents
// are rendered as React text, never interpreted as HTML or executable markup.
function webUrl(value: string | undefined): string | null {
  if (!value) return null;
  try {
    const url = new URL(value);
    return ["https:", "http:"].includes(url.protocol) && !url.username && !url.password
      ? url.href
      : null;
  } catch {
    return null;
  }
}

export function parseZhihuOutput(run: RuntimeCLIRunRequest): ZhihuOutput | null {
  if (run.status !== "completed" || run.exit_code !== 0 || run.truncated || !run.output) return null;
  // Keep unknown CLI entries on the generic output path, even if their JSON
  // happens to resemble a Zhihu response.
  if (run.cli_key !== "zhihu" && run.cli_key !== "zhihu-search" && run.cli_key !== "zhihu-hot") return null;
  let json: unknown;
  try {
    json = JSON.parse(run.output);
  } catch {
    return null;
  }
  if (run.cli_key === "zhihu") {
    const result = statusSchema.safeParse(json);
    return result.success ? { kind: "status", data: result.data } : null;
  }

  const error = z.object({ Code: z.number(), Message: optionalText }).safeParse(json);
  if (error.success && error.data.Code !== 0) {
    return { kind: "error", code: error.data.Code, message: error.data.Message ?? "" };
  }
  if (run.cli_key === "zhihu-hot") {
    const result = hotSchema.safeParse(json);
    if (!result.success) return null;
    return {
      kind: "hot",
      items: result.data.Data.Items.map((item) => ({
        title: item.Title,
        url: webUrl(item.Url),
        text: item.Summary ?? "",
      })),
    };
  }
  const result = searchSchema.safeParse(json);
  if (!result.success) return null;
  return {
    kind: "search",
    hasMore: result.data.Data.HasMore === true,
    items: result.data.Data.Items.map((item) => ({
      title: item.Title,
      url: webUrl(item.Url),
      text: item.ContentText ?? "",
      contentType: item.ContentType,
      author: item.AuthorName,
      votes: item.VoteUpCount,
      comments: item.CommentCount,
    })),
  };
}
