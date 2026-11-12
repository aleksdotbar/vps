import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useMutation } from "@tanstack/react-query";
import {
  createLink,
  Link,
  type LinkOptions,
  useRouter,
} from "@tanstack/react-router";
import { format, formatDistanceToNow } from "date-fns";
import {
  CheckIcon,
  CircleAlertIcon,
  CircleCheckIcon,
  CircleDashedIcon,
  CopyIcon,
  LoaderCircleIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { Fragment, type ReactNode, useState } from "react";

import { Badge } from "@/components/ui/badge";
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import {
  Pagination,
  PaginationContent,
  PaginationItem,
  PaginationNext,
  PaginationPrevious,
} from "@/components/ui/pagination";
import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Spinner } from "@/components/ui/spinner";

import {
  type Environment,
  ServerReadiness,
} from "../../gen/ts/panel/v1/panel_pb";
import { errorMessage, queryClient, unauthenticated } from "./api";

/** Router link rendered as a shadcn button. */
export const ButtonLink = createLink(function ButtonAnchor(
  props: Omit<React.ComponentProps<typeof Button>, "render" | "nativeButton"> &
    React.ComponentProps<"a">,
) {
  return <Button nativeButton={false} render={<a />} {...props} />;
});

export type Crumb = {
  label: string;
  link: LinkOptions;
};

/** Page frame: sidebar trigger and breadcrumbs, then the title row with actions on the right. */
export function Page({
  title,
  description,
  crumbs = [],
  actions,
  children,
}: {
  title: string;
  description?: ReactNode;
  crumbs?: Crumb[];
  actions?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <>
      <header className="flex h-14 shrink-0 items-center gap-2 border-b px-4">
        <SidebarTrigger className="-ml-1" />
        <Separator
          orientation="vertical"
          className="mr-2 data-vertical:h-4 data-vertical:self-auto"
        />
        <Breadcrumb>
          <BreadcrumbList>
            {crumbs.map((crumb) => (
              <Fragment key={crumb.label}>
                <BreadcrumbItem>
                  <BreadcrumbLink render={<Link {...crumb.link} />}>
                    {crumb.label}
                  </BreadcrumbLink>
                </BreadcrumbItem>
                <BreadcrumbSeparator />
              </Fragment>
            ))}
            <BreadcrumbItem>
              <BreadcrumbPage>{title}</BreadcrumbPage>
            </BreadcrumbItem>
          </BreadcrumbList>
        </Breadcrumb>
      </header>
      <div className="mx-auto flex w-full max-w-6xl flex-col gap-6 p-4 md:p-8">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div className="flex min-w-0 flex-col gap-1">
            <h1 className="truncate text-2xl font-semibold tracking-tight">
              {title}
            </h1>
            {description && (
              <div className="text-sm text-muted-foreground">{description}</div>
            )}
          </div>
          {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
        </div>
        {children}
      </div>
    </>
  );
}

export type Tone = "success" | "warning" | "danger" | "pending" | "neutral";

// Status colors follow shadcn's dashboard block: an outline badge with a colored icon.
const tones: Record<Tone, ReactNode> = {
  success: <CircleCheckIcon className="text-emerald-500" />,
  warning: <TriangleAlertIcon className="text-amber-500" />,
  danger: <CircleAlertIcon className="text-red-500" />,
  pending: <LoaderCircleIcon className="animate-spin text-sky-500" />,
  neutral: <CircleDashedIcon className="text-muted-foreground" />,
};

export function StatusBadge({
  tone,
  children,
}: {
  tone: Tone;
  children: ReactNode;
}) {
  return (
    <Badge variant="outline">
      {tones[tone]}
      {children}
    </Badge>
  );
}

export function EnvironmentStatus({
  environment,
}: {
  environment: Environment;
}) {
  if (environment.pendingChanges)
    return <StatusBadge tone="warning">Changes pending</StatusBadge>;
  if (environment.currentReleaseId)
    return <StatusBadge tone="success">Deployed</StatusBadge>;
  return <StatusBadge tone="neutral">Not deployed</StatusBadge>;
}

const readiness: Record<ServerReadiness, [string, Tone]> = {
  [ServerReadiness.UNSPECIFIED]: ["Unknown", "neutral"],
  [ServerReadiness.SETUP_PENDING]: ["Setup pending", "warning"],
  [ServerReadiness.READY]: ["Ready", "success"],
  [ServerReadiness.UNREACHABLE]: ["Unreachable", "danger"],
  [ServerReadiness.NEEDS_ATTENTION]: ["Needs attention", "warning"],
};

export function Readiness({ value }: { value: ServerReadiness }) {
  const [label, tone] = readiness[value] ?? readiness[0];
  return <StatusBadge tone={tone}>{label}</StatusBadge>;
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
      <span className="text-muted-foreground">
        {date ? "Time unavailable" : fallback}
      </span>
    );
  return (
    <time dateTime={date.toISOString()} title={format(date, "PPpp")}>
      {prefix && `${prefix} `}
      {formatDistanceToNow(date, { addSuffix: true })}
    </time>
  );
}

/** Label/value pairs; shadcn has no description list. */
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
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children || "—"}</dd>
    </div>
  );
}

/** Monospace identifier with a copy button; `short` shows the first characters of long IDs and digests. */
export function Id({
  value,
  short = false,
}: {
  value: string;
  short?: boolean;
}) {
  const [copied, setCopied] = useState(false);
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
    <span className="inline-flex min-w-0 items-center gap-1">
      <code className="truncate font-mono text-xs" title={value}>
        {shown}
      </code>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label={copied ? "Copied" : "Copy"}
        onClick={() =>
          void navigator.clipboard?.writeText(value).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          })
        }
      >
        {copied ? <CheckIcon /> : <CopyIcon />}
      </Button>
    </span>
  );
}

const PreviousLink = createLink(PaginationPrevious);
const NextLink = createLink(PaginationNext);

/** Cursor paging links; the API can't step back, so callers point Previous at the first page. */
export function CursorPagination({
  previous,
  next,
}: {
  previous?: LinkOptions;
  next?: LinkOptions;
}) {
  if (!previous && !next) return null;
  return (
    <Pagination className="justify-start">
      <PaginationContent>
        {previous && (
          <PaginationItem>
            <PreviousLink {...previous} />
          </PaginationItem>
        )}
        {next && (
          <PaginationItem>
            <NextLink {...next} />
          </PaginationItem>
        )}
      </PaginationContent>
    </Pagination>
  );
}

/** shadcn's loading-button pattern: disabled with a Spinner while the action runs. */
export function PendingButton({
  pending,
  children,
  disabled,
  ...props
}: React.ComponentProps<typeof Button> & { pending?: boolean }) {
  return (
    <Button disabled={disabled || pending} {...props}>
      {pending && <Spinner data-icon="inline-start" />}
      {children}
    </Button>
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
    <Empty role="alert" className="min-h-svh">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <CircleAlertIcon />
        </EmptyMedia>
        <EmptyTitle>
          {expired ? "Your session ended" : "This page could not load"}
        </EmptyTitle>
        <EmptyDescription>{errorMessage(error)}</EmptyDescription>
      </EmptyHeader>
      <EmptyContent className="flex-row justify-center">
        <PendingButton
          pending={recover.isPending}
          onClick={() => recover.mutate()}
        >
          {expired ? "Sign in" : "Try again"}
        </PendingButton>
        <ButtonLink variant="outline" to="/projects" search={{ cursor: "" }}>
          Go to projects
        </ButtonLink>
      </EmptyContent>
    </Empty>
  );
}
