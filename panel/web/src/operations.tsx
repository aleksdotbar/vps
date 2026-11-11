import { Button } from "@cloudflare/kumo/components/button";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { OperationOutcome } from "../../gen/ts/panel/v1/panel_pb";
import { errorMessage, operationQuery, operationsQuery } from "./api";
import { Detail, Details, Empty, ObservedTime, PageTitle } from "./components";
import {
  operationActive,
  operationKind,
  operationOutcome,
  OperationStatus,
} from "./operation-status";

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
  const active = list.data.operations.some(operationActive);

  return (
    <>
      <PageTitle
        title="Operations"
        description="Recorded deployment, rollback and server enrollment status."
      >
        <Button
          disabled={list.isFetching}
          onClick={() => {
            void list.refetch();
          }}
        >
          {list.isFetching ? "Refreshing…" : "Refresh"}
        </Button>
      </PageTitle>
      {(environment || server) && (
        <div className="form-actions">
          {environment && (
            <Link
              to="/environments/$environmentId"
              params={{ environmentId: environment }}
              search={{ services: "", releases: "" }}
            >
              Environment
            </Link>
          )}
          {server && (
            <Link to="/servers/$serverId" params={{ serverId: server }}>
              Server
            </Link>
          )}
          <Link
            to="/operations"
            search={{ environment: "", server: "", cursor: "" }}
          >
            All operations
          </Link>
        </div>
      )}
      {list.error && (
        <p role="alert">
          Refresh failed. Showing earlier status; current outcomes may differ.{" "}
          {errorMessage(list.error)}
        </p>
      )}
      {active && (
        <p className="muted">
          {!list.error
            ? "Active operations update automatically while this tab is visible."
            : "Automatic refresh paused. Refresh to check current status."}
        </p>
      )}
      <section aria-label="Operation history" aria-busy={list.isFetching}>
        {list.data.operations.length ? (
          <ul className="resource-list">
            {list.data.operations.map((operation) => (
              <li key={operation.id}>
                <div>
                  <Link
                    className="resource-name"
                    to="/operations/$operationId"
                    params={{ operationId: operation.id }}
                  >
                    {operationKind(operation.kind)}
                  </Link>
                  <p>
                    {operation.environmentId ? (
                      <Link
                        to="/environments/$environmentId"
                        params={{ environmentId: operation.environmentId }}
                        search={{ services: "", releases: "" }}
                      >
                        Environment {operation.environmentId}
                      </Link>
                    ) : operation.serverId ? (
                      <Link
                        to="/servers/$serverId"
                        params={{ serverId: operation.serverId }}
                      >
                        Server {operation.serverId}
                      </Link>
                    ) : (
                      "Target unavailable"
                    )}
                  </p>
                  <p>{operationOutcome(operation.outcome)}</p>
                  {operation.message && <p>{operation.message}</p>}
                  <code className="digest">{operation.id}</code>
                </div>
                <div className="resource-meta">
                  <OperationStatus value={operation.state} />
                  <span className="muted">
                    Updated <ObservedTime value={operation.updatedAt} />
                  </span>
                </div>
              </li>
            ))}
          </ul>
        ) : (
          <Empty
            title={
              cursor ? "No operations on this page" : "No operations recorded"
            }
          >
            {cursor
              ? "Return to newest operations or refresh this page."
              : "Accepted operations will appear here. Prepared releases alone do not confirm deployment."}
          </Empty>
        )}
      </section>
      <nav className="pagination" aria-label="Operation pages">
        {cursor && (
          <Link to="/operations" search={{ environment, server, cursor: "" }}>
            Newest operations
          </Link>
        )}
        {list.data.page?.nextPageToken && (
          <Link
            to="/operations"
            search={{
              environment,
              server,
              cursor: list.data.page.nextPageToken,
            }}
          >
            Older operations
          </Link>
        )}
      </nav>
    </>
  );
}

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
      <Empty title="Operation unavailable">
        Return to operation history and refresh the list.
      </Empty>
    );

  return (
    <>
      <Link
        className="back-link"
        to="/operations"
        search={{
          environment: operation.environmentId,
          server: operation.environmentId ? "" : operation.serverId,
          cursor: "",
        }}
      >
        Operation history
      </Link>
      <PageTitle
        title={operationKind(operation.kind)}
        description={operation.id}
      >
        <Button
          disabled={record.isFetching}
          onClick={() => {
            void record.refetch();
          }}
        >
          {record.isFetching ? "Refreshing…" : "Refresh"}
        </Button>
      </PageTitle>
      {record.error && (
        <p role="alert">
          Refresh failed. Showing earlier status; current outcome may differ.{" "}
          {errorMessage(record.error)}
        </p>
      )}
      {operationActive(operation) && (
        <p className="muted">
          {!record.error
            ? "Status updates automatically while this tab is visible."
            : "Automatic refresh paused. Refresh to check current status."}
        </p>
      )}
      <Details>
        <Detail label="Status">
          <OperationStatus value={operation.state} />
        </Detail>
        <Detail label="Outcome">{operationOutcome(operation.outcome)}</Detail>
        <Detail label="Created">
          <ObservedTime value={operation.createdAt} />
        </Detail>
        <Detail label="Last update">
          <ObservedTime value={operation.updatedAt} />
        </Detail>
        {operation.environmentId && (
          <Detail label="Environment">
            <Link
              to="/environments/$environmentId"
              params={{ environmentId: operation.environmentId }}
              search={{ services: "", releases: "" }}
            >
              {operation.environmentId}
            </Link>
          </Detail>
        )}
        {operation.serverId && (
          <Detail label="Server">
            <Link
              to="/servers/$serverId"
              params={{ serverId: operation.serverId }}
            >
              {operation.serverId}
            </Link>
          </Detail>
        )}
        <Detail label="Selected release">
          {operation.releaseId ? (
            <Link
              to="/releases/$releaseId"
              params={{ releaseId: operation.releaseId }}
            >
              {operation.releaseId}
            </Link>
          ) : (
            "No release recorded"
          )}
        </Detail>
        {operation.previousReleaseId && (
          <Detail label="Previous release">
            <Link
              to="/releases/$releaseId"
              params={{ releaseId: operation.previousReleaseId }}
            >
              {operation.previousReleaseId}
            </Link>
          </Detail>
        )}
      </Details>
      {operation.message && <p className="notice">{operation.message}</p>}
      {(operation.outcome === OperationOutcome.UNKNOWN ||
        operation.outcome === OperationOutcome.PARTIAL) && (
        <p className="notice">
          The remote result is unresolved. Do not assume failure or retry the
          operation blindly. Conflicting work must wait for reconciliation.
        </p>
      )}
      <p className="muted">
        Operation status records its result. Check environment service
        observations for application health.
      </p>
    </>
  );
}
