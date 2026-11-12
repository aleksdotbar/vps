import {
  ActivityIcon,
  BoxIcon,
  GitBranchIcon,
  GitCommitHorizontalIcon,
  KeyRoundIcon,
  PackageIcon,
  SettingsIcon,
} from "lucide-react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Fragment } from "react";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardAction,
  CardContent,
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
  ItemMedia,
  ItemSeparator,
  ItemTitle,
} from "@/components/ui/item";

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
  StatusBadge,
  type Tone,
  CursorPagination,
} from "./components";
import { DeploymentAction } from "./deployment-action";

type Status = [string, Tone];

function serviceStatus(service: Service): Status {
  switch (service.state) {
    case ServiceState.RUNNING:
      if (service.health === ServiceHealth.UNHEALTHY)
        return ["Unhealthy", "danger"];
      if (service.health === ServiceHealth.STARTING)
        return ["Starting", "pending"];
      if (service.health === ServiceHealth.HEALTHY)
        return ["Healthy", "success"];
      return ["Running", "success"];
    case ServiceState.STARTING:
      return ["Starting", "pending"];
    case ServiceState.EXITED:
      return ["Exited", "danger"];
    case ServiceState.STOPPED:
      return ["Stopped", "neutral"];
    default:
      return ["Unknown", "neutral"];
  }
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
        <Empty>
          <EmptyHeader>
            <EmptyTitle>Environment not found</EmptyTitle>
          </EmptyHeader>
          <EmptyContent>
            <ButtonLink to="/projects">Back to projects</ButtonLink>
          </EmptyContent>
        </Empty>
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
  const page = (search: { services: string; releases: string }) => ({
    to: "/environments/$environmentId" as const,
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
          description={
            <span className="inline-flex flex-wrap items-center gap-1">
              <GitBranchIcon className="size-3.5" aria-hidden="true" />
              {settings.branch} on
              <Link
                to="/servers/$serverId"
                params={{ serverId: settings.serverId }}
                className="text-foreground underline-offset-4 hover:underline"
              >
                {server?.name ?? "server"}
              </Link>
            </span>
          }
          crumbs={[
            {
              label: "Projects",
              link: { to: "/projects" },
            },
            {
              label: project?.name ?? "Project",
              link: {
                to: "/projects/$projectId",
                params: { projectId: environment.projectId },
              },
            },
          ]}
          actions={
            <>
              <EnvironmentStatus environment={environment} />
              <ButtonLink
                variant="outline"
                to="/environments/$environmentId/settings"
                params={{ environmentId: id }}
              >
                <SettingsIcon data-icon="inline-start" />
                Settings
              </ButtonLink>
              <ButtonLink
                variant="outline"
                to="/environments/$environmentId/secrets"
                params={{ environmentId: id }}
              >
                <KeyRoundIcon data-icon="inline-start" />
                Secrets
              </ButtonLink>
              {trigger}
            </>
          }
        >
          {panel}
          {refreshFailed && (
            <Alert>
              <AlertTitle>
                Couldn’t refresh this environment. Some details may be out of
                date.
              </AlertTitle>
            </Alert>
          )}
          {environment.pendingChanges && (
            <Alert>
              <AlertTitle>Changes not deployed yet</AlertTitle>
              <AlertDescription>
                Saved settings or secrets apply on the next deployment.
              </AlertDescription>
            </Alert>
          )}
          <div className="grid gap-6 lg:grid-cols-3">
            <div className="flex flex-col gap-6 lg:col-span-2">
              <Card>
                <CardHeader>
                  <CardTitle>Services</CardTitle>
                  <CardAction>
                    <Button
                      variant="ghost"
                      size="sm"
                      nativeButton={false}
                      render={
                        <Link to="/operations" search={{ environment: id }} />
                      }
                    >
                      <ActivityIcon data-icon="inline-start" />
                      Activity
                    </Button>
                  </CardAction>
                </CardHeader>
                <CardContent className="flex flex-col gap-3">
                  {serviceList.services.length ? (
                    <ItemGroup>
                      {serviceList.services.map((service, index) => {
                        const [label, tone] = serviceStatus(service);
                        return (
                          <Fragment key={service.name}>
                            {index > 0 && <ItemSeparator />}
                            <Item size="sm">
                              <ItemMedia variant="icon">
                                <BoxIcon />
                              </ItemMedia>
                              <ItemContent>
                                <ItemTitle>{service.name}</ItemTitle>
                                <ItemDescription>
                                  {service.observedImageDigest ? (
                                    <Id
                                      value={service.observedImageDigest}
                                      short
                                    />
                                  ) : (
                                    "No image yet"
                                  )}
                                </ItemDescription>
                              </ItemContent>
                              <ItemActions>
                                <StatusBadge tone={tone}>{label}</StatusBadge>
                                <span className="text-xs text-muted-foreground">
                                  <ObservedTime value={service.observedAt} />
                                </span>
                              </ItemActions>
                            </Item>
                          </Fragment>
                        );
                      })}
                    </ItemGroup>
                  ) : (
                    <Empty>
                      <EmptyHeader>
                        <EmptyMedia variant="icon">
                          <PackageIcon />
                        </EmptyMedia>
                        <EmptyTitle>No services running</EmptyTitle>
                        <EmptyDescription>
                          {environment.currentReleaseId
                            ? "Service status appears shortly after a deployment."
                            : "Deploy this environment to start its services."}
                        </EmptyDescription>
                      </EmptyHeader>
                    </Empty>
                  )}
                  <CursorPagination
                    previous={
                      services ? page({ services: "", releases }) : undefined
                    }
                    next={
                      serviceList.page?.nextPageToken
                        ? page({
                            services: serviceList.page.nextPageToken,
                            releases,
                          })
                        : undefined
                    }
                  />
                </CardContent>
              </Card>
              <Card>
                <CardHeader>
                  <CardTitle>Releases</CardTitle>
                </CardHeader>
                <CardContent className="flex flex-col gap-3">
                  {releaseList.releases.length ? (
                    <ItemGroup>
                      {releaseList.releases.map((release, index) => (
                        <Fragment key={release.id}>
                          {index > 0 && <ItemSeparator />}
                          <Item
                            size="sm"
                            className="relative hover:bg-muted/50 has-[a:focus-visible]:bg-muted/50"
                          >
                            <ItemMedia variant="icon">
                              <GitCommitHorizontalIcon />
                            </ItemMedia>
                            <ItemContent>
                              <ItemTitle>
                                <Link
                                  to="/releases/$releaseId"
                                  params={{ releaseId: release.id }}
                                  className="font-mono outline-none after:absolute after:inset-0"
                                >
                                  {release.build?.sourceSha.slice(0, 7) ||
                                    "Release"}
                                </Link>
                                {release.id ===
                                  environment.currentReleaseId && (
                                  <Badge variant="secondary">Current</Badge>
                                )}
                                {release.id ===
                                  environment.previousReleaseId && (
                                  <Badge variant="outline">Previous</Badge>
                                )}
                              </ItemTitle>
                              <ItemDescription>
                                {[
                                  release.build?.branch,
                                  `${release.images.length} ${release.images.length === 1 ? "image" : "images"}`,
                                ]
                                  .filter(Boolean)
                                  .join(" · ")}
                              </ItemDescription>
                            </ItemContent>
                            <ItemActions className="text-xs text-muted-foreground">
                              <ObservedTime value={release.createdAt} />
                            </ItemActions>
                          </Item>
                        </Fragment>
                      ))}
                    </ItemGroup>
                  ) : (
                    <Empty>
                      <EmptyHeader>
                        <EmptyMedia variant="icon">
                          <GitCommitHorizontalIcon />
                        </EmptyMedia>
                        <EmptyTitle>No releases yet</EmptyTitle>
                        <EmptyDescription>
                          Each deployment records a release you can roll back
                          to.
                        </EmptyDescription>
                      </EmptyHeader>
                    </Empty>
                  )}
                  <CursorPagination
                    previous={
                      releases ? page({ services, releases: "" }) : undefined
                    }
                    next={
                      releaseList.page?.nextPageToken
                        ? page({
                            services,
                            releases: releaseList.page.nextPageToken,
                          })
                        : undefined
                    }
                  />
                </CardContent>
              </Card>
            </div>
            <Card className="h-fit">
              <CardHeader>
                <CardTitle>Overview</CardTitle>
              </CardHeader>
              <CardContent>
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
                              className="underline-offset-4 hover:underline"
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
                    <code className="font-mono text-xs">
                      {settings.composeOverridePath}
                    </code>
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
                        className="underline-offset-4 hover:underline"
                      >
                        Add secrets
                      </Link>
                    )}
                  </Detail>
                </Details>
              </CardContent>
            </Card>
          </div>
        </Page>
      )}
    </DeploymentAction>
  );
}
