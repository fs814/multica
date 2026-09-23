"use client";

import { useState } from "react";
import { CheckCircle2, ExternalLink, Info, MessageSquare, ThumbsUp } from "lucide-react";
import { parseZhihuOutput, type ZhihuOutput, type ZhihuSearchItem } from "@multica/core/clis";
import type { RuntimeCLIRunRequest } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../i18n";

export function CLIRunOutput({ run }: { run: RuntimeCLIRunRequest }) {
  const { t } = useT("clis");
  const parsed = parseZhihuOutput(run);
  const output = (
    <pre className="bg-muted mt-2 max-h-96 overflow-auto rounded-md p-3 text-caption whitespace-pre-wrap break-words">
      {run.output || t(($) => $.result.no_output)}
    </pre>
  );

  return (
    <div className="mt-4 min-w-0 space-y-4 border-t pt-4">
      {parsed?.kind === "status" && <ZhihuStatus data={parsed.data} />}
      {parsed?.kind === "hot" && (
        <section aria-label={t(($) => $.hot.title)} className="space-y-4">
          <h3 className="text-body font-medium">{t(($) => $.hot.count, { count: parsed.items.length })}</h3>
          <p className="text-muted-foreground text-caption">{t(($) => $.hot.hint)}</p>
          {parsed.items.length === 0 ? (
            <p className="text-muted-foreground py-6 text-body">{t(($) => $.hot.empty)}</p>
          ) : (
            <ol className="space-y-3">
              {parsed.items.map((item, index) => (
                <li key={`${index}:${item.url ?? item.title}`}>
                  <SearchItem item={item} rank={index + 1} />
                </li>
              ))}
            </ol>
          )}
        </section>
      )}
      {parsed?.kind === "search" && (
        <section aria-label={t(($) => $.search.title)} className="space-y-4">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <h3 className="text-body font-medium">
              {t(($) => $.search.count, { count: parsed.items.length })}
            </h3>
            {run.params?.query && <p className="text-muted-foreground text-caption break-all">{run.params.query}</p>}
          </div>
          {parsed.items.length === 0 ? (
            <p className="text-muted-foreground py-6 text-body">{t(($) => $.search.empty)}</p>
          ) : (
            <ol className="space-y-3">
              {parsed.items.map((item, index) => (
                <li key={`${index}:${item.url ?? item.title}`}>
                  <SearchItem item={item} />
                </li>
              ))}
            </ol>
          )}
          {parsed.hasMore && <p className="text-muted-foreground text-caption">{t(($) => $.search.more_available)}</p>}
        </section>
      )}
      {parsed?.kind === "error" && (
        <div role="alert" className="text-destructive space-y-1 text-body">
          <p>{run.cli_key === "zhihu-hot" ? t(($) => $.hot.failed, { code: parsed.code }) : t(($) => $.search.failed, { code: parsed.code })}</p>
          {parsed.message && <p className="break-words">{parsed.message}</p>}
        </div>
      )}
      {run.truncated && (
        <p role="status" className="text-muted-foreground text-caption">
          {t(($) => $.result.truncated, { bytes: run.output_bytes ?? 0 })}
        </p>
      )}
      {parsed ? (
        <details className="text-caption">
          <summary className="text-muted-foreground cursor-pointer py-1 hover:text-foreground">{t(($) => $.result.raw_output)}</summary>
          {output}
        </details>
      ) : output}
      <details className="text-caption">
        <summary className="text-muted-foreground cursor-pointer py-1 hover:text-foreground">{t(($) => $.result.execution_details)}</summary>
        <div className="text-muted-foreground mt-2 flex flex-wrap gap-x-4 gap-y-2">
          {typeof run.exit_code === "number" && <span>{t(($) => $.result.exit_code, { code: run.exit_code })}</span>}
          {typeof run.duration_ms === "number" && <span>{t(($) => $.result.duration, { ms: run.duration_ms })}</span>}
          {run.resolved_argv?.length ? (
            <p className="w-full break-all">{t(($) => $.result.command)}: <code>{run.resolved_argv.join(" ")}</code></p>
          ) : null}
        </div>
      </details>
    </div>
  );
}

function SearchItem({ item, rank }: { item: ZhihuSearchItem; rank?: number }) {
  const { t } = useT("clis");
  const [expanded, setExpanded] = useState(false);
  const longText = item.text.length > 240;
  const contentType = item.contentType === "Answer"
    ? t(($) => $.search.answer)
    : item.contentType === "Article" ? t(($) => $.search.article) : item.contentType;

  return (
    <article className="min-w-0 rounded-lg border p-4">
      <div className="text-muted-foreground mb-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-caption">
        {rank !== undefined && <span className="bg-muted text-foreground rounded px-1.5 py-0.5 font-medium">{t(($) => $.hot.rank, { rank })}</span>}
        {contentType && <span className="bg-muted rounded px-1.5 py-0.5">{contentType}</span>}
        {item.author && <span>{item.author}</span>}
        {item.votes !== undefined && <span className="inline-flex items-center gap-1"><ThumbsUp aria-hidden="true" className="size-3.5" />{t(($) => $.search.votes, { count: item.votes })}</span>}
        {item.comments !== undefined && <span className="inline-flex items-center gap-1"><MessageSquare aria-hidden="true" className="size-3.5" />{t(($) => $.search.comments, { count: item.comments })}</span>}
      </div>
      <h4 className="text-body font-semibold break-words">
        {item.url ? <a className="hover:underline underline-offset-4" href={item.url} target="_blank" rel="noopener noreferrer">{item.title}</a> : item.title}
      </h4>
      {item.text && <p className="mt-2 text-body leading-relaxed whitespace-pre-wrap break-words">{longText && !expanded ? `${item.text.slice(0, 240)}...` : item.text}</p>}
      <div className="mt-3 flex flex-wrap items-center gap-4">
        {longText && (
          <Button variant="ghost" size="sm" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>
            {expanded ? t(($) => $.search.collapse) : t(($) => $.search.expand)}
          </Button>
        )}
        {item.url && <a className="text-primary inline-flex items-center gap-1 text-caption hover:underline" href={item.url} target="_blank" rel="noopener noreferrer">{t(($) => $.search.open_original)}<ExternalLink aria-hidden="true" className="size-3.5" /></a>}
      </div>
    </article>
  );
}

function ZhihuStatus({ data }: { data: Extract<ZhihuOutput, { kind: "status" }>["data"] }) {
  const { t } = useT("clis");
  const ready = data.ok && data.installed && data.auth.configured && data.cli.compatible === true && data.next_action === "ready";
  const unknown = t(($) => $.status.unknown);
  const source = data.auth.source === "keychain" ? t(($) => $.status.keychain)
    : data.auth.source === "environment" || data.auth.source === "env" ? t(($) => $.status.environment) : unknown;
  const fields = [
    { label: t(($) => $.status.cli_version), value: data.cli.current_version || unknown,
      detail: !data.installed ? t(($) => $.status.not_installed) : data.cli.compatible === true ? t(($) => $.status.compatible) : data.cli.compatible === false ? t(($) => $.status.incompatible) : unknown,
      update: data.cli.update_available === true ? data.cli.latest_version || unknown : null },
    { label: t(($) => $.status.skill_version), value: data.skill?.current_version || unknown,
      detail: data.skill?.update_available === false ? t(($) => $.status.up_to_date) : "",
      update: data.skill?.update_available === true ? data.skill.latest_version || unknown : null },
    { label: t(($) => $.status.auth), value: data.auth.configured ? t(($) => $.status.configured) : t(($) => $.status.not_configured),
      detail: data.auth.configured ? source : t(($) => $.status.secret_required), update: null },
    { label: t(($) => $.status.update_check), value: data.update_check?.status === "verified" ? t(($) => $.status.verified) : t(($) => $.status.not_verified),
      detail: data.cli.update_available === false ? t(($) => $.status.cli_up_to_date) : "", update: null },
  ];
  return (
    <section aria-label={t(($) => $.status.title)} className="space-y-4">
      <div className="flex items-start gap-2">
        {ready ? <CheckCircle2 aria-hidden="true" className="text-primary mt-0.5 size-5 shrink-0" /> : <Info aria-hidden="true" className="mt-0.5 size-5 shrink-0" />}
        <div>
          <h3 className="text-body font-semibold">{ready ? t(($) => $.status.ready) : t(($) => $.status.needs_attention)}</h3>
          <p className="text-muted-foreground mt-1 text-body">{ready ? t(($) => $.status.ready_hint) : !data.auth.configured ? t(($) => $.status.auth_hint) : t(($) => $.status.check_hint)}</p>
        </div>
      </div>
      <dl className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {fields.map((field) => (
          <div key={field.label} className="min-w-0 rounded-lg border p-3">
            <dt className="text-muted-foreground text-caption">{field.label}</dt>
            <dd className="mt-1 text-body font-medium break-words">{field.value}</dd>
            {field.detail && <dd className="text-muted-foreground mt-1 text-caption">{field.detail}</dd>}
            {field.update && <dd className="text-primary mt-1 text-caption">{t(($) => $.status.update_available, { version: field.update })}</dd>}
          </div>
        ))}
      </dl>
    </section>
  );
}
