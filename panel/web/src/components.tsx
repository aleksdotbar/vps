import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
  Button as KumoButton,
  type ButtonProps as KumoButtonProps,
  LinkButton,
} from "@cloudflare/kumo/components/button";
import {
  ArrowClockwiseIcon,
  CheckIcon,
  CubeIcon,
  CopyIcon,
  InfoIcon,
  WarningCircleIcon,
  WarningIcon,
  CheckCircleIcon,
} from "@phosphor-icons/react";
import { createLink, Link, useRouter } from "@tanstack/react-router";
import { format, formatDistanceToNow } from "date-fns";
import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";

import {
  type Environment,
  ServerReadiness,
} from "../../gen/ts/panel/v1/panel_pb";
import { errorMessage, queryClient, unauthenticated } from "./api";

/**
 * Shows feedback only for work that is actually slow: nothing appears before
 * `delay`, and once visible it stays at least `minimum` to avoid flicker.
 */
function useDelayedFlag(active: boolean, delay = 350, minimum = 450) {
  const [visible, setVisible] = useState(false);
  const shownAt = useRef(0);
  useEffect(() => {
    if (active && !visible) {
      const timer = setTimeout(() => {
        shownAt.current = Date.now();
        setVisible(true);
      }, delay);
      return () => clearTimeout(timer);
    }
    if (!active && visible) {
      const remaining = Math.max(0, minimum - (Date.now() - shownAt.current));
      const timer = setTimeout(() => setVisible(false), remaining);
      return () => clearTimeout(timer);
    }
  }, [active, visible, delay, minimum]);
  return visible;
}

/** Router link rendered as a kumo button. */
export const ButtonLink = createLink(LinkButton);

export function EnvironmentStatus({
  environment,
}: {
  environment: Environment;
}) {
  if (environment.pendingChanges)
    return <Badge tone="warning">Changes pending</Badge>;
  if (environment.currentReleaseId)
    return <Badge tone="success">Deployed</Badge>;
  return <Badge>Not deployed</Badge>;
}

type ButtonProps = KumoButtonProps & { pending?: boolean };

/** Kumo button whose size never changes while pending. */
export function Button({
  pending = false,
  children,
  disabled,
  className,
  ...props
}: ButtonProps) {
  const spinner = useDelayedFlag(pending);
  return (
    <KumoButton
      {...(props as KumoButtonProps)}
      className={["btn", className].filter(Boolean).join(" ")}
      disabled={disabled || pending}
      aria-busy={pending || undefined}
      data-spinner={spinner || undefined}
    >
      {children != null && <span className="btn-label">{children}</span>}
      {spinner && (
        <span className="btn-spinner" aria-hidden="true">
          <span className="spinner" />
        </span>
      )}
    </KumoButton>
  );
}

/**
 * Icon-only refresh. Only the operator's own click shows progress; background
 * polling never does.
 */
export function RefreshButton({
  onRefresh,
  pending = false,
  label = "Refresh",
}: {
  onRefresh: () => Promise<unknown> | void;
  pending?: boolean;
  label?: string;
}) {
  const [running, setRunning] = useState(false);
  const busy = pending || running;
  const spinning = useDelayedFlag(busy);
  return (
    <KumoButton
      variant="secondary"
      shape="square"
      aria-label={label}
      title={label}
      disabled={busy}
      aria-busy={busy || undefined}
      onClick={() => {
        const result = onRefresh();
        if (result instanceof Promise) {
          setRunning(true);
          void result.finally(() => setRunning(false));
        }
      }}
      icon={
        <ArrowClockwiseIcon
          size={16}
          className={spinning ? "icon-spin" : undefined}
        />
      }
    />
  );
}

export function PageHeader({
  title,
  description,
  breadcrumbs,
  badge,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  breadcrumbs?: ReactNode[];
  badge?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <header className="page-header">
      <div className="page-header-text">
        {breadcrumbs && breadcrumbs.length > 0 && (
          <nav className="breadcrumbs" aria-label="Breadcrumb">
            {breadcrumbs.map((crumb, index) => (
              <span className="row" key={index}>
                {index > 0 && <span aria-hidden="true">/</span>}
                {crumb}
              </span>
            ))}
          </nav>
        )}
        <h1 tabIndex={-1}>
          {title}
          {badge}
        </h1>
        {description && <p>{description}</p>}
      </div>
      {children && <div className="page-actions">{children}</div>}
    </header>
  );
}

export function Card({
  title,
  description,
  actions,
  footer,
  children,
  flush,
  labelledBy,
}: {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  footer?: ReactNode;
  children?: ReactNode;
  flush?: boolean;
  labelledBy?: string;
}) {
  return (
    <section className="card" aria-labelledby={labelledBy}>
      {(title || actions) && (
        <div className="card-header">
          <div className="stack-sm" style={{ gap: "0.125rem" }}>
            {title && <h2 id={labelledBy}>{title}</h2>}
            {description && <p>{description}</p>}
          </div>
          {actions && <div className="row">{actions}</div>}
        </div>
      )}
      {flush
        ? children
        : children && <div className="card-body">{children}</div>}
      {footer && <div className="card-footer">{footer}</div>}
    </section>
  );
}

export type Tone = "neutral" | "success" | "warning" | "danger" | "info";

export function Badge({
  tone = "neutral",
  active,
  plain,
  children,
}: {
  tone?: Tone;
  active?: boolean;
  plain?: boolean;
  children: ReactNode;
}) {
  const classes = ["badge", `badge-${tone}`];
  if (active) classes.push("badge-active");
  if (plain) classes.push("badge-plain");
  return <span className={classes.join(" ")}>{children}</span>;
}

const bannerIcons = {
  info: InfoIcon,
  warning: WarningIcon,
  danger: WarningCircleIcon,
  success: CheckCircleIcon,
};

export function Banner({
  tone = "info",
  title,
  children,
  actions,
  role,
}: {
  tone?: keyof typeof bannerIcons;
  title?: ReactNode;
  children?: ReactNode;
  actions?: ReactNode;
  role?: "alert" | "status";
}) {
  const Icon = bannerIcons[tone];
  return (
    <div
      className={`banner banner-${tone}`}
      role={role ?? (tone === "danger" ? "alert" : undefined)}
    >
      <Icon size={18} weight="fill" aria-hidden="true" />
      <div className="banner-body">
        {title && <p className="banner-title">{title}</p>}
        {children && <div>{children}</div>}
        {actions && <div className="banner-actions">{actions}</div>}
      </div>
    </div>
  );
}

export function Empty({
  icon,
  title,
  children,
  actions,
}: {
  icon?: ReactNode;
  title: string;
  children?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="empty">
      {icon && <div className="empty-icon">{icon}</div>}
      <h3>{title}</h3>
      {children && <p>{children}</p>}
      {actions && <div className="row">{actions}</div>}
    </div>
  );
}

export function Readiness({ value }: { value: ServerReadiness }) {
  const map: Record<ServerReadiness, [string, Tone]> = {
    [ServerReadiness.UNSPECIFIED]: ["Unknown", "neutral"],
    [ServerReadiness.SETUP_PENDING]: ["Setup pending", "warning"],
    [ServerReadiness.READY]: ["Ready", "success"],
    [ServerReadiness.UNREACHABLE]: ["Unreachable", "danger"],
    [ServerReadiness.NEEDS_ATTENTION]: ["Needs attention", "warning"],
  };
  const [label, tone] = map[value] ?? ["Unknown", "neutral"];
  return <Badge tone={tone}>{label}</Badge>;
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
  if (!value) return <span className="subtle">{fallback}</span>;
  const date = timestampDate(value);
  if (!Number.isFinite(date.getTime()))
    return <span className="subtle">Time unavailable</span>;
  return (
    <time dateTime={date.toISOString()} title={format(date, "PPpp")}>
      {prefix && `${prefix} `}
      {formatDistanceToNow(date, { addSuffix: true })}
    </time>
  );
}

export function Details({
  children,
  stacked,
}: {
  children: ReactNode;
  stacked?: boolean;
}) {
  return <dl className={stacked ? "kv-stacked" : "kv"}>{children}</dl>;
}

export function Detail({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div style={{ display: "contents" }}>
      <dt>{label}</dt>
      <dd>{children || <span className="subtle">—</span>}</dd>
    </div>
  );
}

/** Identifier shown in monospace, truncated with a copy affordance. */
export function Id({
  value,
  short = false,
  label = "identifier",
}: {
  value: string;
  short?: boolean;
  label?: string;
}) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);
  if (!value) return <span className="subtle">—</span>;
  const digest = value.lastIndexOf("sha256:");
  const shown = !short
    ? value
    : digest >= 0
      ? value.slice(digest + 7, digest + 19)
      : value.length > 12
        ? value.slice(0, 7)
        : value;
  return (
    <span className="id">
      <code title={value}>{shown}</code>
      <button
        type="button"
        className="copy"
        aria-label={copied ? `Copied ${label}` : `Copy ${label}`}
        title={copied ? "Copied" : "Copy"}
        onClick={() => {
          void navigator.clipboard
            ?.writeText(value)
            .then(() => setCopied(true));
        }}
      >
        {copied ? <CheckIcon size={14} /> : <CopyIcon size={14} />}
      </button>
    </span>
  );
}

function SkeletonText({ width = "7rem" }: { width?: string }) {
  return (
    <span
      className="skeleton skeleton-text"
      style={{ width }}
      aria-hidden="true"
    />
  );
}

/** Placeholder that appears only when content is genuinely slow. */
export function DelayedSkeleton({
  loading,
  width,
  children,
}: {
  loading: boolean;
  width?: string;
  children: ReactNode;
}) {
  const show = useDelayedFlag(loading, 250, 400);
  if (!loading) return <span className="fade-in">{children}</span>;
  return show ? <SkeletonText width={width} /> : null;
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
  const [pending, setPending] = useState(false);

  async function recover() {
    setPending(true);
    try {
      if (expired) {
        await queryClient.cancelQueries();
        queryClient.clear();
        await router.navigate({ to: "/login", replace: true });
        return;
      }
      await router.invalidate();
      reset?.();
    } finally {
      setPending(false);
    }
  }

  return (
    <section className="standalone" role="alert">
      <Card>
        <Empty
          icon={<WarningCircleIcon size={20} />}
          title={expired ? "Your session ended" : "This page could not load"}
          actions={
            <>
              <Button
                variant="primary"
                pending={pending}
                onClick={() => void recover()}
              >
                {expired ? "Sign in" : "Try again"}
              </Button>
              <Link to="/projects" search={{ cursor: "" }}>
                Go to projects
              </Link>
            </>
          }
        >
          {errorMessage(error)}
        </Empty>
      </Card>
    </section>
  );
}

/** Shell-shaped placeholder for the first load. */
export function InitialLoading() {
  const show = useDelayedFlag(true, 200, 0);
  return (
    <div className="app" aria-busy="true" aria-label="Loading panel">
      <aside className="sidebar">
        <span className="brand">
          <span className="brand-mark" aria-hidden="true">
            <CubeIcon size={16} weight="bold" />
          </span>
          <span className="brand-text">Panel</span>
        </span>
        {show && (
          <span className="nav fade-in" aria-hidden="true">
            {[0, 1, 2, 3].map((item) => (
              <span
                key={item}
                className="skeleton"
                style={{ height: 14, margin: "0.625rem", width: "60%" }}
              />
            ))}
          </span>
        )}
      </aside>
      <main className="main">
        {show && (
          <div className="stack fade-in">
            <span className="skeleton" style={{ height: 14, width: 120 }} />
            <span className="skeleton" style={{ height: 26, width: 220 }} />
            <span
              className="skeleton"
              style={{ height: 180, width: "100%", borderRadius: 10 }}
            />
            <span
              className="skeleton"
              style={{ height: 120, width: "100%", borderRadius: 10 }}
            />
          </div>
        )}
      </main>
    </div>
  );
}
