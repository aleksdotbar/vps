import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { ServiceHealth, ServiceState } from "../../gen/ts/panel/v1/panel_pb";
import {
  environmentQuery,
  releasesQuery,
  serverQuery,
  servicesQuery,
} from "./api";
import { Detail, Details, Empty, ObservedTime, PageTitle } from "./components";
import { DeploymentAction } from "./deployment-action";

const serviceStates: Record<ServiceState, string> = {
  [ServiceState.UNSPECIFIED]: "Unknown",
  [ServiceState.UNKNOWN]: "Unknown",
  [ServiceState.STOPPED]: "Stopped",
  [ServiceState.STARTING]: "Starting",
  [ServiceState.RUNNING]: "Running",
  [ServiceState.EXITED]: "Exited",
};
const healthStates: Record<ServiceHealth, string> = {
  [ServiceHealth.UNSPECIFIED]: "Health unknown",
  [ServiceHealth.UNKNOWN]: "Health unknown",
  [ServiceHealth.STARTING]: "Health check starting",
  [ServiceHealth.HEALTHY]: "Healthy",
  [ServiceHealth.UNHEALTHY]: "Unhealthy",
  [ServiceHealth.NOT_CONFIGURED]: "No health check",
};

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
      <Empty title="Environment unavailable">
        Return to projects and refresh the list.
      </Empty>
    );
  const settings = environment.settings;

  return (
    <>
      <Link
        className="back-link"
        to="/projects/$projectId"
        params={{ projectId: environment.projectId }}
        search={{ cursor: "" }}
      >
        Project environments
      </Link>
      <PageTitle
        title={settings.name}
        description={`Branch: ${settings.branch}`}
      >
        <div className="form-actions">
          <Link
            to="/operations"
            search={{ environment: id, server: "", cursor: "" }}
          >
            Operation history
          </Link>
          <Link
            to="/environments/$environmentId/settings"
            params={{ environmentId: id }}
            search={{ servers: "" }}
          >
            Edit settings
          </Link>
          <Link
            to="/environments/$environmentId/secrets"
            params={{ environmentId: id }}
          >
            Edit secrets
          </Link>
          <span className="status">
            {environment.pendingChanges
              ? "Changes pending"
              : environment.currentReleaseId
                ? "Applied"
                : "Not deployed"}
          </span>
        </div>
      </PageTitle>
      {(record.error || serviceList.error || releaseList.error) && (
        <p role="alert">
          Refresh failed. Some observations may be out of date.
        </p>
      )}
      <Details>
        <Detail label="Server">
          <EnvironmentServer id={settings.serverId} />
        </Detail>
        <Detail label="Compose override">
          <code>{settings.composeOverridePath}</code>
        </Detail>
        <Detail label="Automatic deployment">
          {settings.autoDeploy ? "Enabled" : "Disabled"}
        </Detail>
        <Detail label="Settings version">
          {environment.version.toString()}
        </Detail>
        <Detail label="Current release">
          {environment.currentReleaseId ? (
            <Link
              to="/releases/$releaseId"
              params={{ releaseId: environment.currentReleaseId }}
            >
              {environment.currentReleaseId}
            </Link>
          ) : (
            "Not deployed"
          )}
        </Detail>
        <Detail label="Secret names">
          {environment.secretNames.length
            ? environment.secretNames.join(", ")
            : "None configured"}
        </Detail>
        <Detail label="Secret version">
          {environment.latestSecretVersionId || "Unavailable"}
        </Detail>
      </Details>
      {environment.pendingChanges && (
        <p className="notice">
          Saved settings and secrets take effect on the next deployment.
        </p>
      )}
      <DeploymentAction
        key={environment.id}
        environment={environment}
        refreshFailed={!!record.error}
      />
      <section aria-labelledby="domains-title">
        <h2 id="domains-title">Domains</h2>
        {settings.domains.length ? (
          <ul className="resource-list">
            {settings.domains.map((domain) => (
              <li key={domain.hostname}>
                <span>{domain.hostname}</span>
                <code>
                  {domain.service}:{domain.port}
                </code>
              </li>
            ))}
          </ul>
        ) : (
          <p className="muted">No domains configured.</p>
        )}
      </section>
      <section aria-labelledby="services-title">
        <h2 id="services-title">Services</h2>
        {serviceList.data.services.length ? (
          <ul className="resource-list">
            {serviceList.data.services.map((service) => (
              <li key={service.name}>
                <div>
                  <strong>{service.name}</strong>
                  <p>
                    {serviceStates[service.state] ?? "Unknown"} ·{" "}
                    {healthStates[service.health] ?? "Health unknown"}
                  </p>
                  <code className="digest">
                    {service.observedImageDigest || "No image observed"}
                  </code>
                </div>
                <span className="muted">
                  Observed <ObservedTime value={service.observedAt} />
                </span>
              </li>
            ))}
          </ul>
        ) : (
          <Empty title="No service observations">
            Service state appears after a deployment has been observed.
          </Empty>
        )}
      </section>
      <nav className="pagination" aria-label="Service pages">
        {services && (
          <Link
            to="/environments/$environmentId"
            params={{ environmentId: id }}
            search={{ services: "", releases }}
          >
            First services
          </Link>
        )}
        {serviceList.data.page?.nextPageToken && (
          <Link
            to="/environments/$environmentId"
            params={{ environmentId: id }}
            search={{ services: serviceList.data.page.nextPageToken, releases }}
          >
            More services
          </Link>
        )}
      </nav>
      <section aria-labelledby="releases-title">
        <h2 id="releases-title">Release history</h2>
        {releaseList.data.releases.length ? (
          <ul className="resource-list">
            {releaseList.data.releases.map((release) => (
              <li key={release.id}>
                <div>
                  <Link
                    className="resource-name"
                    to="/releases/$releaseId"
                    params={{ releaseId: release.id }}
                  >
                    <code>{release.build?.sourceSha || release.id}</code>
                  </Link>
                  <p>
                    {release.id === environment.currentReleaseId
                      ? "Current release"
                      : release.id === environment.previousReleaseId
                        ? "Previous release"
                        : "Prepared release"}{" "}
                    · {release.images.length} images
                  </p>
                </div>
                <ObservedTime value={release.createdAt} />
              </li>
            ))}
          </ul>
        ) : (
          <Empty title="No releases yet">
            Verified, prepared releases will appear here.
          </Empty>
        )}
      </section>
      <nav className="pagination" aria-label="Release pages">
        {releases && (
          <Link
            to="/environments/$environmentId"
            params={{ environmentId: id }}
            search={{ services, releases: "" }}
          >
            Newest releases
          </Link>
        )}
        {releaseList.data.page?.nextPageToken && (
          <Link
            to="/environments/$environmentId"
            params={{ environmentId: id }}
            search={{ services, releases: releaseList.data.page.nextPageToken }}
          >
            Older releases
          </Link>
        )}
      </nav>
    </>
  );
}

function EnvironmentServer({ id }: { id: string }) {
  const server = useSuspenseQuery(serverQuery(id)).data.server;
  return (
    <Link to="/servers/$serverId" params={{ serverId: id }}>
      {server?.name || id}
    </Link>
  );
}
