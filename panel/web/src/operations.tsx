import {
  CaretRightIcon,
  CheckIcon,
  PulseIcon,
  WarningCircleIcon,
  XIcon,
} from "@phosphor-icons/react";
import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import {
  type Operation,
  OperationOutcome,
} from "../../gen/ts/panel/v1/panel_pb";
import {
  environmentQuery,
  errorMessage,
  operationQuery,
  operationsQuery,
  projectQuery,
  serverQuery,
} from "./api";
import {
  Banner,
  Card,
  DelayedSkeleton,
  Detail,
  Details,
  Empty,
  Id,
  ButtonLink,
  ObservedTime,
  PageHeader,
  RefreshButton,
} from "./components";
import {
  operationActive,
  operationKind,
  OperationStatus,
  operationSteps,
} from "./operation-status";

/** Resolves an environment to "project / environment" without blocking. */
function EnvironmentLabel({ id }: { id: string }) {
  const environment = useQuery(environmentQuery(id));
  const projectId = environment.data?.environment?.projectId ?? "";
  const project = useQuery({
    ...projectQuery(projectId),
    enabled: !!projectId,
  });
  const name = environment.data?.environment?.settings?.name;
  const loading = environment.isPending || (!!projectId && project.isPending);
  return (
    <DelayedSkeleton loading={loading} width="9rem">
      {project.data?.project?.name && `${project.data.project.name} / `}
      {name ?? "Environment"}
    </DelayedSkeleton>
  );
}

function ServerLabel({ id }: { id: string }) {
  const server = useQuery(serverQuery(id));
  return (
    <DelayedSkeleton loading={server.isPending} width="5rem">
      {server.data?.server?.name ?? "Server"}
    </DelayedSkeleton>
  );
}

function Target({ operation }: { operation: Operation }) {
  if (operation.environmentId)
    return <EnvironmentLabel id={operation.environmentId} />;
  if (operation.serverId) return <ServerLabel id={operation.serverId} />;
  return <span className="subtle">Unknown target</span>;
}

export function Operations({
  environment,
  server,
  cursor,
}: {
  environment: string;
  server: string;
  cursor: string;
}) {
  const list = useSuspenseQuery({
    ...operationsQuery(environment, server, cursor),
    refetchInterval: (query) =>
      !query.state.error && query.state.data?.operations.some(operationActive)
        ? 5_000
        : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
  const filtered = !!(environment || server);

  return (
    <>
      <PageHeader
        title="Activity"
        description={
          filtered ? (
            <span>
              Showing{" "}
              {environment ? (
                <Link
                  to="/environments/$environmentId"
                  params={{ environmentId: environment }}
                  search={{ services: "", releases: "" }}
                >
                  <EnvironmentLabel id={environment} />
                </Link>
              ) : (
                <Link to="/servers/$serverId" params={{ serverId: server }}>
                  <ServerLabel id={server} />
                </Link>
              )}{" "}
              ·{" "}
              <Link
                to="/operations"
                search={{ environment: "", server: "", cursor: "" }}
              >
                Show all
              </Link>
            </span>
          ) : (
            "Deployments, rollbacks and server changes."
          )
        }
      >
        <RefreshButton
          label="Refresh activity"
          onRefresh={() => list.refetch()}
        />
      </PageHeader>
      <div className="stack">
        {list.error && (
          <Banner tone="warning">
            Couldn’t refresh activity. {errorMessage(list.error)}
          </Banner>
        )}
        <section className="card" aria-label="Activity">
          {list.data.operations.length ? (
            <ul className="list">
              {list.data.operations.map((operation) => {
                const active = operationActive(operation);
                return (
                  <li key={operation.id}>
                    <Link
                      className="list-row"
                      to="/operations/$operationId"
                      params={{ operationId: operation.id }}
                    >
                      <span className="list-main">
                        <span className="list-title">
                          {operationKind(operation.kind)}
                          <span className="muted" style={{ fontWeight: 400 }}>
                            <Target operation={operation} />
                          </span>
                        </span>
                        <span className="list-meta">
                          <ObservedTime value={operation.updatedAt} />
                          {operation.message &&
                            operation.message !== "Operation failed." && (
                              <span style={{ overflowWrap: "anywhere" }}>
                                {operation.message}
                              </span>
                            )}
                        </span>
                      </span>
                      <span className="list-aside">
                        <OperationStatus
                          value={operation.state}
                          active={active}
                        />
                        <CaretRightIcon className="chevron" size={16} />
                      </span>
                    </Link>
                  </li>
                );
              })}
            </ul>
          ) : (
            <Empty
              icon={<PulseIcon size={20} />}
              title={cursor ? "No more activity" : "Nothing here yet"}
            >
              Deployments and rollbacks appear here as soon as they start.
            </Empty>
          )}
          {(cursor || list.data.page?.nextPageToken) && (
            <nav className="pagination" aria-label="Activity pages">
              {cursor && (
                <ButtonLink
                  variant="secondary"
                  size="sm"
                  to="/operations"
                  search={{ environment, server, cursor: "" }}
                >
                  Newest
                </ButtonLink>
              )}
              {list.data.page?.nextPageToken && (
                <ButtonLink
                  variant="secondary"
                  size="sm"
                  to="/operations"
                  search={{
                    environment,
                    server,
                    cursor: list.data.page.nextPageToken,
                  }}
                >
                  Older
                </ButtonLink>
              )}
            </nav>
          )}
        </section>
      </div>
    </>
  );
}

const stepIcons = {
  done: <CheckIcon size={12} weight="bold" />,
  failed: <XIcon size={12} weight="bold" />,
  current: (
    <span
      className="spinner"
      style={{ width: 10, height: 10, borderWidth: 1.5 }}
    />
  ),
  pending: null,
};

export function OperationDetail({ id }: { id: string }) {
  const record = useSuspenseQuery({
    ...operationQuery(id),
    refetchInterval: (query) =>
      !query.state.error &&
      query.state.data?.operation &&
      operationActive(query.state.data.operation)
        ? 5_000
        : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });
  const operation = record.data.operation;
  if (!operation)
    return (
      <Card>
        <Empty title="Not found">
          <Link
            to="/operations"
            search={{ environment: "", server: "", cursor: "" }}
          >
            Back to activity
          </Link>
        </Empty>
      </Card>
    );
  const active = operationActive(operation);
  const steps = operationSteps(operation);
  const unresolved =
    operation.outcome === OperationOutcome.UNKNOWN ||
    operation.outcome === OperationOutcome.PARTIAL;

  return (
    <>
      <PageHeader
        breadcrumbs={[
          <Link
            key="activity"
            to="/operations"
            search={{ environment: "", server: "", cursor: "" }}
          >
            Activity
          </Link>,
        ]}
        title={operationKind(operation.kind)}
        badge={<OperationStatus value={operation.state} active={active} />}
        description={
          <span className="row" style={{ gap: "0.25rem 0.5rem" }}>
            <Target operation={operation} />
            <span aria-hidden="true">·</span>
            <ObservedTime value={operation.createdAt} prefix="Started" />
          </span>
        }
      >
        <RefreshButton
          label="Refresh status"
          onRefresh={() => record.refetch()}
        />
      </PageHeader>
      <div className="stack">
        {record.error && (
          <Banner tone="warning">
            Couldn’t refresh status. {errorMessage(record.error)}
          </Banner>
        )}
        {unresolved && (
          <Banner tone="warning" title="Outcome not confirmed yet">
            The panel is still reconciling what happened on the server. Other
            deployments to this environment wait until it’s resolved.
          </Banner>
        )}
        <div className="grid-2">
          <div className="stack">
            <Card title="Progress" labelledBy="progress-title">
              <ol className="steps">
                {steps.map((step) => (
                  <li key={step.title} className={`step step-${step.status}`}>
                    <span className="step-dot" aria-hidden="true">
                      {stepIcons[step.status]}
                    </span>
                    <span>
                      <span className="step-title">
                        {step.title}
                        <span className="visually-hidden">
                          {` (${step.status})`}
                        </span>
                      </span>
                      {step.detail && (
                        <span
                          className="step-detail"
                          style={{ display: "block" }}
                        >
                          {step.detail}
                        </span>
                      )}
                    </span>
                  </li>
                ))}
              </ol>
            </Card>
            {operation.diagnostics.length > 0 && (
              <Card
                title="What went wrong"
                description="The last state and logs of each failing service, captured before cleanup."
                labelledBy="diagnostics-title"
              >
                {operation.diagnostics.map((diagnostic) => (
                  <div className="stack-sm" key={diagnostic.service}>
                    <div className="row">
                      <WarningCircleIcon
                        size={16}
                        style={{ color: "var(--danger-fg)" }}
                        aria-hidden="true"
                      />
                      <strong>{diagnostic.service}</strong>
                      <span className="chip">
                        {diagnostic.state || "unknown"}
                        {diagnostic.state === "exited" &&
                          ` (code ${diagnostic.exitCode})`}
                      </span>
                      {diagnostic.health && (
                        <span className="chip">{diagnostic.health}</span>
                      )}
                    </div>
                    {diagnostic.logTail ? (
                      <pre
                        className="log"
                        tabIndex={0}
                        aria-label={`Last log lines of ${diagnostic.service}`}
                      >
                        {diagnostic.logTail}
                      </pre>
                    ) : (
                      <p className="subtle">No log output was captured.</p>
                    )}
                  </div>
                ))}
              </Card>
            )}
          </div>
          <Card title="Details" labelledBy="operation-details-title">
            <Details stacked>
              {operation.environmentId && (
                <Detail label="Environment">
                  <Link
                    to="/environments/$environmentId"
                    params={{ environmentId: operation.environmentId }}
                    search={{ services: "", releases: "" }}
                  >
                    <EnvironmentLabel id={operation.environmentId} />
                  </Link>
                </Detail>
              )}
              {operation.serverId && (
                <Detail label="Server">
                  <Link
                    to="/servers/$serverId"
                    params={{ serverId: operation.serverId }}
                  >
                    <ServerLabel id={operation.serverId} />
                  </Link>
                </Detail>
              )}
              {operation.releaseId && (
                <Detail label="Release">
                  <Link
                    to="/releases/$releaseId"
                    params={{ releaseId: operation.releaseId }}
                  >
                    View release
                  </Link>
                </Detail>
              )}
              {operation.previousReleaseId && (
                <Detail label="Previous release">
                  <Link
                    to="/releases/$releaseId"
                    params={{ releaseId: operation.previousReleaseId }}
                  >
                    View release
                  </Link>
                </Detail>
              )}
              <Detail label="Last update">
                <ObservedTime value={operation.updatedAt} />
              </Detail>
              <Detail label="ID">
                <Id value={operation.id} short label="operation ID" />
              </Detail>
            </Details>
          </Card>
        </div>
      </div>
    </>
  );
}
