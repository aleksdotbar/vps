import {
  Badge,
  Banner,
  CodeBlock,
  Empty,
  Grid,
  LayerCard,
  Pagination,
  RefreshButton,
  SkeletonLine,
  Table,
  Text,
} from "@cloudflare/kumo";
import {
  PulseIcon,
  WarningCircleIcon,
  WarningIcon,
} from "@phosphor-icons/react";
import { useMutation, useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";

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
    return <SkeletonLine minWidth={80} maxWidth={140} />;
  return (
    <>
      {project.data?.project?.name && `${project.data.project.name} / `}
      {name ?? "Environment"}
    </>
  );
}

function ServerLabel({ id }: { id: string }) {
  const server = useQuery(serverQuery(id));
  if (server.isPending) return <SkeletonLine minWidth={60} maxWidth={100} />;
  return <>{server.data?.server?.name ?? "Server"}</>;
}

function Target({ operation }: { operation: Operation }) {
  if (operation.environmentId)
    return <EnvironmentLabel id={operation.environmentId} />;
  if (operation.serverId) return <ServerLabel id={operation.serverId} />;
  return <>Unknown target</>;
}

function RefreshFailed({ what, error }: { what: string; error: unknown }) {
  return (
    <Banner
      variant="alert"
      icon={<WarningIcon weight="fill" />}
      description={`Couldn’t refresh ${what}. ${errorMessage(error)}`}
    />
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
  const navigate = useNavigate();
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
          aria-label="Refresh activity"
          loading={refresh.isPending}
          onClick={() => refresh.mutate()}
        />
      }
    >
      {filtered && (
        <Text as="p" variant="secondary">
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
        </Text>
      )}
      {list.error && <RefreshFailed what="activity" error={list.error} />}
      {list.data.operations.length ? (
        <Table aria-label="Activity">
          <Table.Header>
            <Table.Row>
              <Table.Head>Operation</Table.Head>
              <Table.Head>Target</Table.Head>
              <Table.Head>Status</Table.Head>
              <Table.Head>Updated</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {list.data.operations.map((operation) => (
              <Table.Row key={operation.id}>
                <Table.Cell>
                  <Link
                    to="/operations/$operationId"
                    params={{ operationId: operation.id }}
                  >
                    {operationKind(operation.kind)}
                  </Link>
                  {operation.message &&
                    operation.message !== "Operation failed." && (
                      <Text variant="secondary" size="sm">
                        {operation.message}
                      </Text>
                    )}
                </Table.Cell>
                <Table.Cell>
                  <Target operation={operation} />
                </Table.Cell>
                <Table.Cell>
                  <OperationStatus
                    value={operation.state}
                    active={operationActive(operation)}
                  />
                </Table.Cell>
                <Table.Cell>
                  <ObservedTime value={operation.updatedAt} />
                </Table.Cell>
              </Table.Row>
            ))}
          </Table.Body>
        </Table>
      ) : (
        <Empty
          icon={<PulseIcon size={48} />}
          title={cursor ? "No more activity" : "Nothing here yet"}
          description="Deployments and rollbacks appear here as soon as they start."
        />
      )}
      {(cursor || nextPageToken) && (
        <Pagination
          controls="simple"
          page={cursor ? 2 : 1}
          hasNextPage={!!nextPageToken}
          setPage={(page) =>
            void navigate({
              to: "/operations",
              search: {
                environment,
                server,
                // The cursor API can't step back, so Previous returns to the newest page.
                cursor: page > (cursor ? 2 : 1) ? nextPageToken : "",
              },
            })
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
        <Empty
          title="Operation not found"
          contents={
            <ButtonLink
              to="/operations"
              search={{ environment: "", server: "", cursor: "" }}
            >
              Back to activity
            </ButtonLink>
          }
        />
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
      crumbs={[{ label: "Activity", href: "/operations" }]}
      actions={
        <RefreshButton
          aria-label="Refresh status"
          loading={refresh.isPending}
          onClick={() => refresh.mutate()}
        />
      }
    >
      {record.error && <RefreshFailed what="status" error={record.error} />}
      {unresolved && (
        <Banner
          variant="alert"
          icon={<WarningIcon weight="fill" />}
          title="Outcome not confirmed yet"
          description="The panel is still reconciling what happened on the server. Other deployments to this environment wait until it’s resolved."
        />
      )}
      <Grid variant="2-1" gap="base">
        <div className="flex flex-col gap-6">
          <LayerCard>
            <LayerCard.Secondary>Status</LayerCard.Secondary>
            <LayerCard.Primary className="flex flex-col gap-2">
              <OperationStatus
                value={operation.state}
                active={operationActive(operation)}
              />
              {message && <Text>{message}</Text>}
            </LayerCard.Primary>
          </LayerCard>
          {operation.diagnostics.length > 0 && (
            <LayerCard>
              <LayerCard.Secondary>What went wrong</LayerCard.Secondary>
              <LayerCard.Primary className="flex flex-col gap-4">
                <Text variant="secondary">
                  The last state and logs of each failing service, captured
                  before cleanup.
                </Text>
                {operation.diagnostics.map((diagnostic) => (
                  <section
                    key={diagnostic.service}
                    className="flex flex-col gap-2"
                    aria-label={`Service ${diagnostic.service}`}
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <WarningCircleIcon
                        size={16}
                        weight="fill"
                        className="text-kumo-danger"
                        aria-hidden="true"
                      />
                      <Text bold as="span">
                        {diagnostic.service}
                      </Text>
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
                      <CodeBlock lang="bash" code={diagnostic.logTail} />
                    ) : (
                      <Text variant="secondary">
                        No log output was captured.
                      </Text>
                    )}
                  </section>
                ))}
              </LayerCard.Primary>
            </LayerCard>
          )}
        </div>
        <LayerCard>
          <LayerCard.Secondary>Details</LayerCard.Secondary>
          <LayerCard.Primary>
            <Details>
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
          </LayerCard.Primary>
        </LayerCard>
      </Grid>
    </Page>
  );
}
