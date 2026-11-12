import {
  CaretRightIcon,
  GearIcon,
  GitBranchIcon,
  KeyIcon,
  PackageIcon,
  PulseIcon,
} from "@phosphor-icons/react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

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
  Badge,
  Banner,
  Card,
  Detail,
  Details,
  Empty,
  EnvironmentStatus,
  Id,
  ButtonLink,
  ObservedTime,
  PageHeader,
  type Tone,
} from "./components";
import { DeploymentAction } from "./deployment-action";

function serviceStatus(service: Service): [string, Tone] {
  switch (service.state) {
    case ServiceState.RUNNING:
      if (service.health === ServiceHealth.UNHEALTHY)
        return ["Unhealthy", "danger"];
      if (service.health === ServiceHealth.STARTING)
        return ["Starting", "info"];
      if (service.health === ServiceHealth.HEALTHY)
        return ["Healthy", "success"];
      return ["Running", "success"];
    case ServiceState.STARTING:
      return ["Starting", "info"];
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
      <Card>
        <Empty title="Environment not found">
          <Link to="/projects" search={{ cursor: "" }}>
            Back to projects
          </Link>
        </Empty>
      </Card>
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

  return (
    <DeploymentAction
      key={environment.id}
      environment={environment}
      refreshFailed={metadataFailed}
      serverName={server?.name}
    >
      {({ trigger, panel }) => (
        <>
          <PageHeader
            breadcrumbs={[
              <Link key="projects" to="/projects" search={{ cursor: "" }}>
                Projects
              </Link>,
              <Link
                key="project"
                to="/projects/$projectId"
                params={{ projectId: environment.projectId }}
                search={{ cursor: "" }}
              >
                {project?.name ?? "Project"}
              </Link>,
            ]}
            title={settings.name}
            badge={<EnvironmentStatus environment={environment} />}
            description={
              <span className="row" style={{ gap: "0.375rem 0.75rem" }}>
                <span className="row" style={{ gap: "0.25rem" }}>
                  <GitBranchIcon size={14} aria-hidden="true" />
                  {settings.branch}
                </span>
                <span>
                  on{" "}
                  <Link
                    to="/servers/$serverId"
                    params={{ serverId: settings.serverId }}
                  >
                    {server?.name ?? "server"}
                  </Link>
                </span>
              </span>
            }
          >
            <ButtonLink
              variant="secondary"
              to="/environments/$environmentId/settings"
              params={{ environmentId: id }}
              search={{ servers: "" }}
            >
              <GearIcon size={16} aria-hidden="true" />
              Settings
            </ButtonLink>
            <ButtonLink
              variant="secondary"
              to="/environments/$environmentId/secrets"
              params={{ environmentId: id }}
            >
              <KeyIcon size={16} aria-hidden="true" />
              Secrets
            </ButtonLink>
            {trigger}
          </PageHeader>
          <div className="stack">
            {panel}
            {refreshFailed && (
              <Banner tone="warning">
                Couldn’t refresh this environment. Some details may be out of
                date.
              </Banner>
            )}
            {environment.pendingChanges && (
              <Banner tone="info" title="Changes not deployed yet">
                Saved settings or secrets apply on the next deployment.
              </Banner>
            )}
            <div className="grid-2">
              <div className="stack">
                <Card
                  title="Services"
                  labelledBy="services-title"
                  flush
                  actions={
                    <Link
                      to="/operations"
                      search={{ environment: id, server: "", cursor: "" }}
                      className="row"
                      style={{ gap: "0.375rem", fontSize: "0.8125rem" }}
                    >
                      <PulseIcon size={14} aria-hidden="true" />
                      Activity
                    </Link>
                  }
                >
                  {serviceList.services.length ? (
                    <ul className="list">
                      {serviceList.services.map((service) => {
                        const [label, tone] = serviceStatus(service);
                        return (
                          <li key={service.name}>
                            <div className="list-row">
                              <span
                                className="empty-icon"
                                style={{ margin: 0 }}
                              >
                                <PackageIcon size={18} aria-hidden="true" />
                              </span>
                              <span className="list-main">
                                <span className="list-title">
                                  {service.name}
                                </span>
                                <span className="list-meta">
                                  {service.observedImageDigest ? (
                                    <Id
                                      value={service.observedImageDigest}
                                      short
                                      label="image digest"
                                    />
                                  ) : (
                                    "No image yet"
                                  )}
                                  <ObservedTime
                                    value={service.observedAt}
                                    prefix="Checked"
                                  />
                                </span>
                              </span>
                              <span className="list-aside">
                                <Badge tone={tone}>{label}</Badge>
                              </span>
                            </div>
                          </li>
                        );
                      })}
                    </ul>
                  ) : (
                    <Empty
                      icon={<PackageIcon size={20} />}
                      title="No services running"
                    >
                      {environment.currentReleaseId
                        ? "Service status appears shortly after a deployment."
                        : "Deploy this environment to start its services."}
                    </Empty>
                  )}
                  {(services || serviceList.page?.nextPageToken) && (
                    <nav className="pagination" aria-label="Service pages">
                      {services && (
                        <ButtonLink
                          variant="secondary"
                          size="sm"
                          to="/environments/$environmentId"
                          params={{ environmentId: id }}
                          search={{ services: "", releases }}
                        >
                          First
                        </ButtonLink>
                      )}
                      {serviceList.page?.nextPageToken && (
                        <ButtonLink
                          variant="secondary"
                          size="sm"
                          to="/environments/$environmentId"
                          params={{ environmentId: id }}
                          search={{
                            services: serviceList.page.nextPageToken,
                            releases,
                          }}
                        >
                          More
                        </ButtonLink>
                      )}
                    </nav>
                  )}
                </Card>
                <Card title="Releases" labelledBy="releases-title" flush>
                  {releaseList.releases.length ? (
                    <ul className="list">
                      {releaseList.releases.map((release) => (
                        <li key={release.id}>
                          <Link
                            className="list-row"
                            to="/releases/$releaseId"
                            params={{ releaseId: release.id }}
                          >
                            <span className="list-main">
                              <span className="list-title">
                                <code>
                                  {release.build?.sourceSha.slice(0, 7) ||
                                    "Release"}
                                </code>
                                {release.id ===
                                  environment.currentReleaseId && (
                                  <Badge tone="success">Current</Badge>
                                )}
                                {release.id ===
                                  environment.previousReleaseId && (
                                  <Badge>Previous</Badge>
                                )}
                              </span>
                              <span className="list-meta">
                                <ObservedTime value={release.createdAt} />
                                <span>
                                  {release.images.length}{" "}
                                  {release.images.length === 1
                                    ? "image"
                                    : "images"}
                                </span>
                              </span>
                            </span>
                            <span className="list-aside">
                              <CaretRightIcon className="chevron" size={16} />
                            </span>
                          </Link>
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <Empty title="No releases yet">
                      Each deployment records a release you can roll back to.
                    </Empty>
                  )}
                  {(releases || releaseList.page?.nextPageToken) && (
                    <nav className="pagination" aria-label="Release pages">
                      {releases && (
                        <ButtonLink
                          variant="secondary"
                          size="sm"
                          to="/environments/$environmentId"
                          params={{ environmentId: id }}
                          search={{ services, releases: "" }}
                        >
                          Newest
                        </ButtonLink>
                      )}
                      {releaseList.page?.nextPageToken && (
                        <ButtonLink
                          variant="secondary"
                          size="sm"
                          to="/environments/$environmentId"
                          params={{ environmentId: id }}
                          search={{
                            services,
                            releases: releaseList.page.nextPageToken,
                          }}
                        >
                          Older
                        </ButtonLink>
                      )}
                    </nav>
                  )}
                </Card>
              </div>
              <div className="stack">
                <Card title="Overview" labelledBy="overview-title">
                  <Details stacked>
                    <Detail label="Domains">
                      {settings.domains.length ? (
                        <ul className="stack-sm">
                          {settings.domains.map((domain) => (
                            <li key={domain.hostname}>
                              <a
                                href={`https://${domain.hostname}`}
                                target="_blank"
                                rel="noreferrer"
                              >
                                {domain.hostname}
                              </a>{" "}
                              <span className="chip">
                                {domain.service}:{domain.port}
                              </span>
                            </li>
                          ))}
                        </ul>
                      ) : (
                        <span className="muted">Private (no domains)</span>
                      )}
                    </Detail>
                    <Detail label="Auto-deploy">
                      {settings.autoDeploy
                        ? `On — pushes to ${settings.branch} deploy`
                        : "Off"}
                    </Detail>
                    <Detail label="Compose override">
                      <code>{settings.composeOverridePath}</code>
                    </Detail>
                    <Detail label="Secrets">
                      {environment.secretNames.length ? (
                        <span className="row" style={{ gap: "0.25rem" }}>
                          {environment.secretNames.map((name) => (
                            <span className="chip" key={name}>
                              {name}
                            </span>
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
                </Card>
              </div>
            </div>
          </div>
        </>
      )}
    </DeploymentAction>
  );
}
