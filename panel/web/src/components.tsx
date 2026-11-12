import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
  Badge,
  Breadcrumbs,
  Button,
  Empty,
  InlineCopyText,
  LinkButton,
  Text,
} from "@cloudflare/kumo";
import { WarningCircleIcon } from "@phosphor-icons/react";
import { useMutation } from "@tanstack/react-query";
import { createLink, useRouter } from "@tanstack/react-router";
import { format, formatDistanceToNow } from "date-fns";
import { Fragment, type ReactNode } from "react";

import {
  type Environment,
  ServerReadiness,
} from "../../gen/ts/panel/v1/panel_pb";
import { errorMessage, queryClient, unauthenticated } from "./api";
import { PageHeader } from "./components/kumo/page-header/page-header";

/** Router link rendered as a kumo button. */
export const ButtonLink = createLink(LinkButton);

export type Crumb = { label: string; href: string };

/** Page frame: kumo PageHeader block, an optional actions row, then content. */
export function Page({
  title,
  description,
  crumbs = [],
  actions,
  children,
}: {
  title: string;
  description?: string;
  crumbs?: Crumb[];
  actions?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="flex w-full max-w-6xl flex-col gap-6 p-4 md:p-6">
      <PageHeader
        title={title}
        description={description}
        breadcrumbs={
          <Breadcrumbs>
            {crumbs.map((crumb) => (
              <Fragment key={crumb.href}>
                <Breadcrumbs.Link href={crumb.href}>
                  {crumb.label}
                </Breadcrumbs.Link>
                <Breadcrumbs.Separator />
              </Fragment>
            ))}
            <Breadcrumbs.Current>{title}</Breadcrumbs.Current>
          </Breadcrumbs>
        }
      />
      {actions && <div className="flex flex-wrap gap-2 pl-3">{actions}</div>}
      {children}
    </div>
  );
}

export function EnvironmentStatus({
  environment,
}: {
  environment: Environment;
}) {
  if (environment.pendingChanges)
    return <Badge variant="warning">Changes pending</Badge>;
  if (environment.currentReleaseId)
    return <Badge variant="success">Deployed</Badge>;
  return <Badge variant="neutral">Not deployed</Badge>;
}

const readiness: Record<
  ServerReadiness,
  [string, "neutral" | "warning" | "success" | "error"]
> = {
  [ServerReadiness.UNSPECIFIED]: ["Unknown", "neutral"],
  [ServerReadiness.SETUP_PENDING]: ["Setup pending", "warning"],
  [ServerReadiness.READY]: ["Ready", "success"],
  [ServerReadiness.UNREACHABLE]: ["Unreachable", "error"],
  [ServerReadiness.NEEDS_ATTENTION]: ["Needs attention", "warning"],
};

export function Readiness({ value }: { value: ServerReadiness }) {
  const [label, variant] = readiness[value] ?? readiness[0];
  return <Badge variant={variant}>{label}</Badge>;
}

export function ObservedTime({
  value,
  prefix,
  fallback = "Not observed yet",
}: {
  value?: Timestamp;
  prefix?: string;
  fallback?: string;
}) {
  const date = value && timestampDate(value);
  if (!date || !Number.isFinite(date.getTime()))
    return (
      <Text as="span" variant="secondary">
        {date ? "Time unavailable" : fallback}
      </Text>
    );
  return (
    <time dateTime={date.toISOString()} title={format(date, "PPpp")}>
      {prefix && `${prefix} `}
      {formatDistanceToNow(date, { addSuffix: true })}
    </time>
  );
}

/** Label/value pairs; kumo has no description list. */
export function Details({ children }: { children: ReactNode }) {
  return (
    <dl className="grid grid-cols-1 gap-x-6 gap-y-3 text-sm sm:grid-cols-[max-content_1fr]">
      {children}
    </dl>
  );
}

export function Detail({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="contents">
      <dt className="text-kumo-subtle">{label}</dt>
      <dd className="min-w-0 break-words">{children || "—"}</dd>
    </div>
  );
}

/** Copyable identifier; `short` shows the first characters of long IDs and digests. */
export function Id({
  value,
  short = false,
}: {
  value: string;
  short?: boolean;
}) {
  if (!value) return <>—</>;
  const digest = value.lastIndexOf("sha256:");
  const shown = !short
    ? value
    : digest >= 0
      ? value.slice(digest + 7, digest + 19)
      : value.length > 12
        ? value.slice(0, 7)
        : value;
  return (
    <InlineCopyText value={value} title={value}>
      {shown}
    </InlineCopyText>
  );
}

export function ErrorPage({
  error,
  reset,
}: {
  error: unknown;
  reset?: () => void;
}) {
  const router = useRouter();
  const expired = unauthenticated(error);
  const recover = useMutation({
    mutationFn: async () => {
      if (expired) {
        await queryClient.cancelQueries();
        queryClient.clear();
        await router.navigate({ to: "/login", replace: true });
        return;
      }
      await router.invalidate();
      reset?.();
    },
  });

  return (
    <div role="alert" className="p-4 md:p-6">
      <Empty
        icon={<WarningCircleIcon size={48} />}
        title={expired ? "Your session ended" : "This page could not load"}
        description={errorMessage(error)}
        contents={
          <div className="flex items-center gap-2">
            <Button
              variant="primary"
              loading={recover.isPending}
              onClick={() => recover.mutate()}
            >
              {expired ? "Sign in" : "Try again"}
            </Button>
            <ButtonLink variant="ghost" to="/projects" search={{ cursor: "" }}>
              Go to projects
            </ButtonLink>
          </div>
        }
      />
    </div>
  );
}
