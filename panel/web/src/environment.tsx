import {
  Badge,
  Banner,
  Empty,
  Grid,
  LayerCard,
  Pagination,
  Table,
  Text,
} from "@cloudflare/kumo";
import {
  GearIcon,
  GitBranchIcon,
  KeyIcon,
  PackageIcon,
  PulseIcon,
} from "@phosphor-icons/react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";

import {
  type Environment as EnvironmentRecord,
  type ListReleasesResponse,
  type ListServicesResponse,
  type Service,
  ServiceHealth,
  ServiceState,
} from "../../gen/ts/panel/v1/panel_pb";
import {
  environmentQuery,
  projectQuery,
  releasesQuery,
  serverQuery,
  servicesQuery,
} from "./api";
import {
  ButtonLink,
  Detail,
  Details,
  EnvironmentStatus,
  Id,
  ObservedTime,
  Page,
} from "./components";
import { DeploymentAction } from "./deployment-action";

type Status = [string, "success" | "info" | "error" | "neutral"];

function serviceStatus(service: Service): Status {
  switch (service.state) {
    case ServiceState.RUNNING:
      if (service.health === ServiceHealth.UNHEALTHY)
        return ["Unhealthy", "error"];
      if (service.health === ServiceHealth.STARTING)
        return ["Starting", "info"];
      if (service.health === ServiceHealth.HEALTHY)
        return ["Healthy", "success"];
      return ["Running", "success"];
    case ServiceState.STARTING:
      return ["Starting", "info"];
    case ServiceState.EXITED:
      return ["Exited", "error"];
    case ServiceState.STOPPED:
      return ["Stopped", "neutral"];
    default:
      return ["Unknown", "neutral"];
  }
}

/** Cursor paging: Previous returns to the first page because the API can't step back. */
function CursorPages({
  cursor,
  next,
  go,
}: {
  cursor: string;
  next: string;
  go: (cursor: string) => void;
}) {
  if (!cursor && !next) return null;
  const page = cursor ? 2 : 1;
  return (
    <Pagination
      controls="simple"
      page={page}
      hasNextPage={!!next}
      setPage={(target) => go(target > page ? next : "")}
    />
  );
}

export function Environment({
  id,
  services,
  releases,
}: {
  id: string;
  services: string;
  releases: string;
}) {
  const record = useSuspenseQuery({
    ...environmentQuery(id),
    refetchInterval: 30_000,
  });
  const serviceList = useSuspenseQuery({
    ...servicesQuery(id, services),
    refetchInterval: 30_000,
  });
  const releaseList = useSuspenseQuery({
    ...releasesQuery(id, releases),
    refetchInterval: 30_000,
  });
  const environment = record.data.environment;
  if (!environment?.settings)
    return (
      <Page title="Environment not found">
        <Empty
          title="Environment not found"
          contents={
            <ButtonLink to="/projects" search={{ cursor: "" }}>
              Back to projects
            </ButtonLink>
          }
        />
      </Page>
    );
  return (
    <EnvironmentView
      environment={environment}
      services={services}
      releases={releases}
      serviceList={serviceList.data}
      releaseList={releaseList.data}
      refreshFailed={
        !!record.error || !!serviceList.error || !!releaseList.error
      }
      metadataFailed={!!record.error}
    />
  );
}

function EnvironmentView({
  environment,
  services,
  releases,
  serviceList,
  releaseList,
  refreshFailed,
  metadataFailed,
}: {
  environment: EnvironmentRecord;
  services: string;
  releases: string;
  serviceList: ListServicesResponse;
  releaseList: ListReleasesResponse;
  refreshFailed: boolean;
  metadataFailed: boolean;
}) {
  const id = environment.id;
  const settings = environment.settings!;
  const project = useSuspenseQuery(projectQuery(environment.projectId)).data
    .project;
  const server = useSuspenseQuery(serverQuery(settings.serverId)).data.server;
  const navigate = useNavigate();
  const page = (search: { services: string; releases: string }) =>
    void navigate({
      to: "/environments/$environmentId",
      params: { environmentId: id },
      search,
    });

  return (
    <DeploymentAction
      key={environment.id}
      environment={environment}
      refreshFailed={metadataFailed}
      serverName={server?.name}
    >
      {({ trigger, panel }) => (
        <Page
          title={settings.name}
          crumbs={[
            { label: "Projects", href: "/projects" },
            {
              label: project?.name ?? "Project",
              href: `/projects/${environment.projectId}`,
            },
          ]}
          actions={
            <>
              <EnvironmentStatus environment={environment} />
              <ButtonLink
                variant="secondary"
                icon={<GearIcon size={16} />}
                to="/environments/$environmentId/settings"
                params={{ environmentId: id }}
                search={{ servers: "" }}
              >
                Settings
              </ButtonLink>
              <ButtonLink
                variant="secondary"
                icon={<KeyIcon size={16} />}
                to="/environments/$environmentId/secrets"
                params={{ environmentId: id }}
              >
                Secrets
              </ButtonLink>
              {trigger}
            </>
          }
        >
          <Text variant="secondary" as="p">
            <span className="inline-flex items-center gap-1">
              <GitBranchIcon size={14} aria-hidden="true" />
              {settings.branch}
            </span>{" "}
            on{" "}
            <Link
              to="/servers/$serverId"
              params={{ serverId: settings.serverId }}
            >
              {server?.name ?? "server"}
            </Link>
          </Text>
          {panel}
          {refreshFailed && (
            <Banner
              variant="alert"
              title="Couldn’t refresh this environment. Some details may be out of date."
            />
          )}
          {environment.pendingChanges && (
            <Banner
              title="Changes not deployed yet"
              description="Saved settings or secrets apply on the next deployment."
            />
          )}
          <Grid variant="2-1" gap="base">
            <div className="flex flex-col gap-6">
              <LayerCard>
                <LayerCard.Secondary className="flex items-center justify-between">
                  Services
                  <Link
                    to="/operations"
                    search={{ environment: id, server: "", cursor: "" }}
                    className="inline-flex items-center gap-1 text-sm"
                  >
                    <PulseIcon size={14} aria-hidden="true" />
                    Activity
                  </Link>
                </LayerCard.Secondary>
                <LayerCard.Primary className="flex flex-col gap-3 p-0">
                  {serviceList.services.length ? (
                    <Table>
                      <Table.Header>
                        <Table.Row>
                          <Table.Head>Service</Table.Head>
                          <Table.Head>Image</Table.Head>
                          <Table.Head>Checked</Table.Head>
                          <Table.Head>Status</Table.Head>
                        </Table.Row>
                      </Table.Header>
                      <Table.Body>
                        {serviceList.services.map((service) => {
                          const [label, variant] = serviceStatus(service);
                          return (
                            <Table.Row key={service.name}>
                              <Table.Cell>{service.name}</Table.Cell>
                              <Table.Cell>
                                {service.observedImageDigest ? (
                                  <Id
                                    value={service.observedImageDigest}
                                    short
                                  />
                                ) : (
                                  "No image yet"
                                )}
                              </Table.Cell>
                              <Table.Cell>
                                <ObservedTime value={service.observedAt} />
                              </Table.Cell>
                              <Table.Cell>
                                <Badge variant={variant}>{label}</Badge>
                              </Table.Cell>
                            </Table.Row>
                          );
                        })}
                      </Table.Body>
                    </Table>
                  ) : (
                    <Empty
                      size="sm"
                      icon={<PackageIcon size={32} />}
                      title="No services running"
                      description={
                        environment.currentReleaseId
                          ? "Service status appears shortly after a deployment."
                          : "Deploy this environment to start its services."
                      }
                    />
                  )}
                  <CursorPages
                    cursor={services}
                    next={serviceList.page?.nextPageToken ?? ""}
                    go={(cursor) => page({ services: cursor, releases })}
                  />
                </LayerCard.Primary>
              </LayerCard>
              <LayerCard>
                <LayerCard.Secondary>Releases</LayerCard.Secondary>
                <LayerCard.Primary className="flex flex-col gap-3 p-0">
                  {releaseList.releases.length ? (
                    <Table>
                      <Table.Header>
                        <Table.Row>
                          <Table.Head>Commit</Table.Head>
                          <Table.Head>Created</Table.Head>
                          <Table.Head>Images</Table.Head>
                        </Table.Row>
                      </Table.Header>
                      <Table.Body>
                        {releaseList.releases.map((release) => (
                          <Table.Row key={release.id}>
                            <Table.Cell>
                              <span className="flex items-center gap-2">
                                <Link
                                  to="/releases/$releaseId"
                                  params={{ releaseId: release.id }}
                                  className="font-mono"
                                >
                                  {release.build?.sourceSha.slice(0, 7) ||
                                    "Release"}
                                </Link>
                                {release.id ===
                                  environment.currentReleaseId && (
                                  <Badge variant="success">Current</Badge>
                                )}
                                {release.id ===
                                  environment.previousReleaseId && (
                                  <Badge variant="neutral">Previous</Badge>
                                )}
                              </span>
                            </Table.Cell>
                            <Table.Cell>
                              <ObservedTime value={release.createdAt} />
                            </Table.Cell>
                            <Table.Cell>{release.images.length}</Table.Cell>
                          </Table.Row>
                        ))}
                      </Table.Body>
                    </Table>
                  ) : (
                    <Empty
                      size="sm"
                      title="No releases yet"
                      description="Each deployment records a release you can roll back to."
                    />
                  )}
                  <CursorPages
                    cursor={releases}
                    next={releaseList.page?.nextPageToken ?? ""}
                    go={(cursor) => page({ services, releases: cursor })}
                  />
                </LayerCard.Primary>
              </LayerCard>
            </div>
            <LayerCard>
              <LayerCard.Secondary>Overview</LayerCard.Secondary>
              <LayerCard.Primary>
                <Details>
                  <Detail label="Domains">
                    {settings.domains.length ? (
                      <ul className="flex flex-col gap-1">
                        {settings.domains.map((domain) => (
                          <li
                            key={domain.hostname}
                            className="flex flex-wrap items-center gap-2"
                          >
                            <a
                              href={`https://${domain.hostname}`}
                              target="_blank"
                              rel="noreferrer"
                            >
                              {domain.hostname}
                            </a>
                            <Badge variant="outline">
                              {domain.service}:{domain.port}
                            </Badge>
                          </li>
                        ))}
                      </ul>
                    ) : (
                      "Private (no domains)"
                    )}
                  </Detail>
                  <Detail label="Auto-deploy">
                    {settings.autoDeploy
                      ? `On — pushes to ${settings.branch} deploy`
                      : "Off"}
                  </Detail>
                  <Detail label="Compose override">
                    <Text variant="mono">{settings.composeOverridePath}</Text>
                  </Detail>
                  <Detail label="Secrets">
                    {environment.secretNames.length ? (
                      <span className="flex flex-wrap gap-1">
                        {environment.secretNames.map((name) => (
                          <Badge variant="outline" key={name}>
                            {name}
                          </Badge>
                        ))}
                      </span>
                    ) : (
                      <Link
                        to="/environments/$environmentId/secrets"
                        params={{ environmentId: id }}
                      >
                        Add secrets
                      </Link>
                    )}
                  </Detail>
                </Details>
              </LayerCard.Primary>
            </LayerCard>
          </Grid>
        </Page>
      )}
    </DeploymentAction>
  );
}
