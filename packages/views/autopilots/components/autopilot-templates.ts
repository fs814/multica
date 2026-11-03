import {
  BarChart3,
  Bug,
  FileSearch,
  GitPullRequest,
  Newspaper,
  Shield,
  type IconComponent,
} from "@multica/ui/icons";
import type { ScheduleConfig } from "./schedule-editor/model";

// Templates offered by the empty state and the Autopilots panel.
// Prompts stay raw English because they're injected directly into the
// agent's task input.

export type AutopilotTemplateId =
  | "daily_news"
  | "pr_review"
  | "bug_triage"
  | "weekly_progress"
  | "dependency_audit"
  | "documentation_check";

export interface AutopilotTemplate {
  id: AutopilotTemplateId;
  prompt: string;
  icon: IconComponent;
  schedule: Pick<ScheduleConfig, "time" | "days">;
}

const WEEKDAYS: ScheduleConfig["days"] = {
  kind: "weekly",
  daysOfWeek: [1, 2, 3, 4, 5],
};
const MONDAY: ScheduleConfig["days"] = { kind: "weekly", daysOfWeek: [1] };

export const AUTOPILOT_TEMPLATES: AutopilotTemplate[] = [
  {
    id: "daily_news",
    prompt: `1. Search the web for news and announcements published today only (strictly today's date)
2. Filter for topics relevant to our team and industry
3. For each item, write a short summary including: title, source, key takeaways
4. Compile everything into a single digest post
5. Post the digest as a comment on this issue and @mention all workspace members`,
    icon: Newspaper,
    schedule: { time: { kind: "at", time: "09:00" }, days: { kind: "every" } },
  },
  {
    id: "pr_review",
    prompt: `1. List all open pull requests in the repository
2. Identify PRs that have been open for more than 24 hours without a review
3. For each stale PR, note the author, age, and a one-line summary of the change
4. Post a comment on this issue listing all stale PRs with links
5. @mention the team to remind them to review`,
    icon: GitPullRequest,
    schedule: { time: { kind: "at", time: "10:00" }, days: WEEKDAYS },
  },
  {
    id: "bug_triage",
    prompt: `1. List all backlog issues that have not been prioritized
2. For each issue, read the description and any attached logs or screenshots
3. Assess severity (critical / high / medium / low) based on user impact and scope
4. Set the priority field on the issue accordingly
5. Add a comment explaining your assessment and suggested next steps`,
    icon: Bug,
    schedule: { time: { kind: "at", time: "09:00" }, days: WEEKDAYS },
  },
  {
    id: "weekly_progress",
    prompt: `1. Gather quality evidence from the past 7 days: failed, reopened, blocked, or repeatedly patched issues; incidents; customer impact; and temporary workarounds. Link every source.
2. Write a weekly quality report that separates confirmed facts, hypotheses, recurring patterns, and unresolved questions.
3. Look for a meeting transcript attached to this issue or linked from the project. If none exists, publish the pre-meeting report, list the missing transcript as an explicit gate, and stop.
4. Reconcile the transcript with the pre-meeting report. Record where the initial conclusion changed, the evidence discussed, and the final decision.
5. Create follow-up issues only for confirmed actions. Give each one an owner, acceptance criteria, evidence links, and one category: immediate fix, long-term fix, product change, or operations change. Use staged child issues for dependencies and leave later stages in backlog.
6. If the project has a rules or evolution repository, open a reviewable PR containing only confirmed, reusable lessons. Never turn speculation into a rule.
7. Post the final report on this issue with links to actions and rule changes, plus every item that remains unverified.`,
    icon: BarChart3,
    schedule: { time: { kind: "at", time: "17:00" }, days: MONDAY },
  },
  {
    id: "dependency_audit",
    prompt: `1. Run dependency audit tools on the project (npm audit, go vuln check, etc.)
2. Identify any packages with known security vulnerabilities
3. List outdated packages that are more than 2 major versions behind
4. For each finding, note the severity, affected package, and recommended fix
5. Post a summary report as a comment with actionable items`,
    icon: Shield,
    schedule: { time: { kind: "at", time: "08:00" }, days: MONDAY },
  },
  {
    id: "documentation_check",
    prompt: `1. Treat the repository as the source of truth. Compare merged code, API routes, CLI help, built-in skills, and tests from the past 7 days with the published documentation.
2. Record only confirmed documentation gaps. For each gap, link the code evidence, affected user workflow, and the command or behavior that proves it.
3. Create tracked child issues for each confirmed gap. Put drafting in stage 1, CLI or behavior verification in stage 2, and editorial review in stage 3. Assign each issue to the appropriate agent and leave later stages in backlog until the prior gate closes.
4. Require drafts to use executable CLI steps instead of console screenshots wherever the CLI supports the workflow. Keep unsupported console-only steps explicit.
5. Verify every command against current --help output and, when safe, an isolated test workspace. Add or update automated tests when documentation depends on parsing, validation, or generated output.
6. Publish through a reviewable PR only after the verification and editorial gates pass. Do not mark a child issue done merely because a draft exists.
7. Post a final comment on this issue with the gap inventory, verification evidence, PR links, and anything that could not be validated.`,
    icon: FileSearch,
    schedule: { time: { kind: "at", time: "14:00" }, days: MONDAY },
  },
];
