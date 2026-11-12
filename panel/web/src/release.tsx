import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import type { Release } from "../../gen/ts/panel/v1/panel_pb";
import { environmentQuery, releaseQuery } from "./api";
import { Detail, Details, Empty, ObservedTime, PageTitle } from "./components";
import { DeploymentAction } from "./deployment-action";

export function ReleaseDetail({ id }: { id: string }) {
  const record = useSuspenseQuery(releaseQuery(id));
  const release = record.data.release;
  if (!release)
    return (
      <Empty title="Release unavailable">
        Return to projects and open the environment's release history.
      </Empty>
    );
  return <ReleaseSnapshot release={release} refreshFailed={!!record.error} />;
}

function ReleaseSnapshot({
  release,
  refreshFailed,
}: {
  release: Release;
  refreshFailed: boolean;
}) {
  const environmentId = release.environmentId;
  const environmentRecord = useSuspenseQuery({
    ...environmentQuery(environmentId),
    refetchInterval: 30_000,
  });
  const environment = environmentRecord.data.environment;
  const build = release.build;
  const position = !environment
    ? "Release selection unknown"
    : release.id === environment.currentReleaseId
      ? "Current release"
      : release.id === environment?.previousReleaseId
        ? "Previous release"
        : "Prepared release";

  return (
    <>
      <Link
        className="back-link"
        to="/environments/$environmentId"
        params={{ environmentId }}
        search={{ services: "", releases: "" }}
      >
        {environment?.settings?.name || "Environment"}
      </Link>
      <PageTitle title="Release details" description={release.id}>
        <span className="status">{position}</span>
      </PageTitle>
      {(refreshFailed || environmentRecord.error) && (
        <p role="alert">
          Refresh failed. Showing previously loaded metadata; current release
          selection may have changed.
        </p>
      )}
      <p className="muted">
        This immutable snapshot records prepared configuration. It does not
        establish current service health or confirm deployment.
      </p>
      <Details>
        <Detail label="Created">
          <ObservedTime value={release.createdAt} />
        </Detail>
        <Detail label="Source SHA">
          <code>{build?.sourceSha || "Unavailable"}</code>
        </Detail>
        <Detail label="Branch">{build?.branch || "Unavailable"}</Detail>
        <Detail label="GitHub repository ID">
          {build?.githubRepositoryId.toString() || "Unavailable"}
        </Detail>
        <Detail label="Settings version">
          {release.environmentVersion.toString()}
        </Detail>
        <Detail label="Binding version">
          {release.bindingVersion.toString()}
        </Detail>
        <Detail label="Secret version">
          {release.secretVersionId || "Unavailable"}
        </Detail>
        <Detail label="Configuration SHA-256">
          <code>{release.configurationSha256 || "Unavailable"}</code>
        </Detail>
      </Details>
      {environment && (
        <DeploymentAction
          key={release.id}
          environment={environment}
          release={release}
          refreshFailed={refreshFailed || !!environmentRecord.error}
        />
      )}
      <section aria-labelledby="release-images-title">
        <h2 id="release-images-title">Service images</h2>
        {release.images.length ? (
          <ul className="resource-list">
            {release.images.map((image) => (
              <li key={image.service}>
                <div>
                  <strong>{image.service}</strong>
                  <code className="digest">{image.imageDigest}</code>
                </div>
              </li>
            ))}
          </ul>
        ) : (
          <p className="muted">No image metadata recorded.</p>
        )}
      </section>
      <section aria-labelledby="release-domains-title">
        <h2 id="release-domains-title">Captured domains</h2>
        {release.domains.length ? (
          <ul className="resource-list">
            {release.domains.map((domain) => (
              <li key={domain.hostname}>
                <span>{domain.hostname}</span>
                <code>
                  {domain.service}:{domain.port}
                </code>
              </li>
            ))}
          </ul>
        ) : (
          <p className="muted">No domains captured.</p>
        )}
      </section>
    </>
  );
}
