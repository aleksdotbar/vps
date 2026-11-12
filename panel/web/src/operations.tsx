import { useMutation, useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ActivityIcon,
  CircleAlertIcon,
  RefreshCwIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { Fragment } from "react";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemSeparator,
  ItemTitle,
} from "@/components/ui/item";
import { Skeleton } from "@/components/ui/skeleton";

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
  ButtonLink,
  Detail,
  Details,
  Id,
  ObservedTime,
  Page,
  CursorPagination,
} from "./components";
import {
  operationActive,
  operationKind,
  OperationStatus,
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
  if (environment.isPending || (!!projectId && project.isPending))
    return <Skeleton className="inline-block h-4 w-28 align-middle" />;
  return (
    <>
      {project.data?.project?.name && `${project.data.project.name} / `}
      {name ?? "Environment"}
    </>
  );
}

function ServerLabel({ id }: { id: string }) {
  const server = useQuery(serverQuery(id));
  if (server.isPending)
    return <Skeleton className="inline-block h-4 w-20 align-middle" />;
  return <>{server.data?.server?.name ?? "Server"}</>;
}

function Target({ operation }: { operation: Operation }) {
  if (operation.environmentId)
    return <EnvironmentLabel id={operation.environmentId} />;
  if (operation.serverId) return <ServerLabel id={operation.serverId} />;
  return <>Unknown target</>;
}

function RefreshButton({
  label,
  pending,
  onClick,
}: {
  label: string;
  pending: boolean;
  onClick: () => void;
}) {
  return (
    <Button
      variant="outline"
      size="icon"
      aria-label={label}
      disabled={pending}
      onClick={onClick}
    >
      <RefreshCwIcon className={pending ? "animate-spin" : undefined} />
    </Button>
  );
}

function RefreshFailed({ what, error }: { what: string; error: unknown }) {
  return (
    <Alert>
      <TriangleAlertIcon />
      <AlertDescription>
        Couldn’t refresh {what}. {errorMessage(error)}
      </AlertDescription>
    </Alert>
  );
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
  const refresh = useMutation({ mutationFn: () => list.refetch() });
  const nextPageToken = list.data.page?.nextPageToken ?? "";
  const filtered = !!(environment || server);

  return (
    <Page
      title="Activity"
      description={
        filtered ? undefined : "Deployments, rollbacks and server changes."
      }
      actions={
        <RefreshButton
          label="Refresh activity"
          pending={refresh.isPending}
          onClick={() => refresh.mutate()}
        />
      }
    >
      {filtered && (
        <p className="text-sm text-muted-foreground">
          Showing{" "}
          {environment ? (
            <Link
              className="text-foreground underline-offset-4 hover:underline"
              to="/environments/$environmentId"
              params={{ environmentId: environment }}
              search={{ services: "", releases: "" }}
            >
              <EnvironmentLabel id={environment} />
            </Link>
          ) : (
            <Link
              className="text-foreground underline-offset-4 hover:underline"
              to="/servers/$serverId"
              params={{ serverId: server }}
            >
              <ServerLabel id={server} />
            </Link>
          )}{" "}
          ·{" "}
          <Link
            className="text-foreground underline-offset-4 hover:underline"
            to="/operations"
            search={{ environment: "", server: "", cursor: "" }}
          >
            Show all
          </Link>
        </p>
      )}
      {list.error && <RefreshFailed what="activity" error={list.error} />}
      {list.data.operations.length ? (
        <ItemGroup aria-label="Activity">
          {list.data.operations.map((operation, index) => (
            <Fragment key={operation.id}>
              {index > 0 && <ItemSeparator className="my-0" />}
              <Item
                render={
                  <Link
                    to="/operations/$operationId"
                    params={{ operationId: operation.id }}
                  />
                }
              >
                <ItemContent className="min-w-0">
                  <ItemTitle>
                    {operationKind(operation.kind)}
                    <span className="font-normal text-muted-foreground">
                      <Target operation={operation} />
                    </span>
                  </ItemTitle>
                  {operation.message &&
                    operation.message !== "Operation failed." && (
                      <ItemDescription className="line-clamp-1">
                        {operation.message}
                      </ItemDescription>
                    )}
                </ItemContent>
                <ItemActions>
                  <OperationStatus
                    value={operation.state}
                    active={operationActive(operation)}
                  />
                  <span className="w-28 text-right text-sm text-muted-foreground">
                    <ObservedTime value={operation.updatedAt} />
                  </span>
                </ItemActions>
              </Item>
            </Fragment>
          ))}
        </ItemGroup>
      ) : (
        <Empty className="border">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <ActivityIcon />
            </EmptyMedia>
            <EmptyTitle>
              {cursor ? "No more activity" : "Nothing here yet"}
            </EmptyTitle>
            <EmptyDescription>
              Deployments and rollbacks appear here as soon as they start.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
      {(cursor || nextPageToken) && (
        <CursorPagination
          previous={
            cursor
              ? {
                  to: "/operations",
                  search: { environment, server, cursor: "" },
                }
              : undefined
          }
          next={
            nextPageToken
              ? {
                  to: "/operations",
                  search: { environment, server, cursor: nextPageToken },
                }
              : undefined
          }
        />
      )}
    </Page>
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
  const refresh = useMutation({ mutationFn: () => record.refetch() });
  const operation = record.data.operation;
  if (!operation)
    return (
      <Page title="Not found">
        <Empty className="border">
          <EmptyHeader>
            <EmptyTitle>Operation not found</EmptyTitle>
          </EmptyHeader>
          <EmptyContent>
            <ButtonLink
              variant="outline"
              to="/operations"
              search={{ environment: "", server: "", cursor: "" }}
            >
              Back to activity
            </ButtonLink>
          </EmptyContent>
        </Empty>
      </Page>
    );
  const unresolved =
    operation.outcome === OperationOutcome.UNKNOWN ||
    operation.outcome === OperationOutcome.PARTIAL;
  const message =
    operation.message === "Operation failed." ? "" : operation.message;

  return (
    <Page
      title={operationKind(operation.kind)}
      crumbs={[
        {
          label: "Activity",
          link: { to: "/operations" },
        },
      ]}
      actions={
        <RefreshButton
          label="Refresh status"
          pending={refresh.isPending}
          onClick={() => refresh.mutate()}
        />
      }
    >
      {record.error && <RefreshFailed what="status" error={record.error} />}
      {unresolved && (
        <Alert>
          <TriangleAlertIcon />
          <AlertTitle>Outcome not confirmed yet</AlertTitle>
          <AlertDescription>
            The panel is still reconciling what happened on the server. Other
            deployments to this environment wait until it’s resolved.
          </AlertDescription>
        </Alert>
      )}
      <div className="grid gap-6 lg:grid-cols-3">
        <div className="flex flex-col gap-6 lg:col-span-2">
          <Card>
            <CardHeader>
              <CardTitle>Status</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col items-start gap-2">
              <OperationStatus
                value={operation.state}
                active={operationActive(operation)}
              />
              {message && <p>{message}</p>}
            </CardContent>
          </Card>
          {operation.diagnostics.length > 0 && (
            <Card>
              <CardHeader>
                <CardTitle>What went wrong</CardTitle>
                <CardDescription>
                  The last state and logs of each failing service, captured
                  before cleanup.
                </CardDescription>
              </CardHeader>
              <CardContent className="flex flex-col gap-6">
                {operation.diagnostics.map((diagnostic) => (
                  <section
                    key={diagnostic.service}
                    className="flex flex-col gap-2"
                    aria-label={`Service ${diagnostic.service}`}
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <CircleAlertIcon
                        className="size-4 text-red-500"
                        aria-hidden="true"
                      />
                      <span className="font-medium">{diagnostic.service}</span>
                      <Badge variant="outline">
                        {diagnostic.state || "unknown"}
                        {diagnostic.state === "exited" &&
                          ` (code ${diagnostic.exitCode})`}
                      </Badge>
                      {diagnostic.health && (
                        <Badge variant="outline">{diagnostic.health}</Badge>
                      )}
                    </div>
                    {diagnostic.logTail ? (
                      <pre className="max-h-80 overflow-auto rounded-xl bg-muted p-4 font-mono text-xs whitespace-pre-wrap">
                        {diagnostic.logTail}
                      </pre>
                    ) : (
                      <p className="text-sm text-muted-foreground">
                        No log output was captured.
                      </p>
                    )}
                  </section>
                ))}
              </CardContent>
            </Card>
          )}
        </div>
        <Card className="self-start">
          <CardHeader>
            <CardTitle>Details</CardTitle>
          </CardHeader>
          <CardContent>
            <Details>
              {operation.environmentId && (
                <Detail label="Environment">
                  <Link
                    className="underline-offset-4 hover:underline"
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
                    className="underline-offset-4 hover:underline"
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
                    className="underline-offset-4 hover:underline"
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
                    className="underline-offset-4 hover:underline"
                    to="/releases/$releaseId"
                    params={{ releaseId: operation.previousReleaseId }}
                  >
                    View release
                  </Link>
                </Detail>
              )}
              <Detail label="Started">
                <ObservedTime value={operation.createdAt} />
              </Detail>
              <Detail label="Last update">
                <ObservedTime value={operation.updatedAt} />
              </Detail>
              <Detail label="ID">
                <Id value={operation.id} short />
              </Detail>
            </Details>
          </CardContent>
        </Card>
      </div>
    </Page>
  );
}
