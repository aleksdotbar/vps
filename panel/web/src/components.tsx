import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Button } from "@cloudflare/kumo/components/button";
import { Link, useRouter } from "@tanstack/react-router";
import { format, formatDistanceToNow } from "date-fns";
import type { ReactNode } from "react";

import { ServerReadiness } from "../../gen/ts/panel/v1/panel_pb";
import { errorMessage, queryClient, unauthenticated } from "./api";

export function PageTitle({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children?: ReactNode;
}) {
  return (
    <header className="page-title">
      <div>
        <h1 tabIndex={-1}>{title}</h1>
        {description && <p>{description}</p>}
      </div>
      {children}
    </header>
  );
}

export function Empty({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <div className="empty" role="status">
      <h2>{title}</h2>
      <p>{children}</p>
    </div>
  );
}

export function Readiness({ value }: { value: ServerReadiness }) {
  const labels: Record<ServerReadiness, string> = {
    [ServerReadiness.UNSPECIFIED]: "Unknown",
    [ServerReadiness.SETUP_PENDING]: "Setup pending",
    [ServerReadiness.READY]: "Ready",
    [ServerReadiness.UNREACHABLE]: "Unreachable",
    [ServerReadiness.NEEDS_ATTENTION]: "Needs attention",
  };
  return (
    <span
      className={`status ${value === ServerReadiness.READY ? "status-ready" : ""}`}
    >
      {labels[value] ?? "Unknown"}
    </span>
  );
}

export function ObservedTime({ value }: { value?: Timestamp }) {
  if (!value) return <span className="muted">Not observed</span>;
  const date = timestampDate(value);
  if (!Number.isFinite(date.getTime()))
    return <span className="muted">Observation time unavailable</span>;
  return (
    <time dateTime={date.toISOString()} title={format(date, "PPpp")}>
      {formatDistanceToNow(date, { addSuffix: true })}
    </time>
  );
}

export function Details({ children }: { children: ReactNode }) {
  return <dl className="details">{children}</dl>;
}

export function Detail({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{children || "—"}</dd>
    </div>
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

  async function recover() {
    if (expired) {
      await queryClient.cancelQueries();
      queryClient.clear();
      await router.navigate({ to: "/login", replace: true });
      return;
    }
    await router.invalidate();
    reset?.();
  }

  return (
    <section className="error-page" role="alert">
      <PageTitle
        title={expired ? "Sign in again" : "Unable to open this page"}
      />
      <p>{errorMessage(error)}</p>
      <Button onClick={() => void recover()}>
        {expired ? "Sign in" : "Try again"}
      </Button>
      <Link to="/projects" search={{ cursor: "" }}>
        Back to projects
      </Link>
    </section>
  );
}

export function InitialLoading() {
  return (
    <main className="initial-loading" aria-busy="true">
      <h1>Hosting panel</h1>
      <p role="status">Opening your panel…</p>
    </main>
  );
}
