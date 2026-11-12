import { PackageIcon } from "@phosphor-icons/react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import type { Release } from "../../gen/ts/panel/v1/panel_pb";
import { environmentQuery, releaseQuery } from "./api";
import {
  Badge,
  Banner,
  Card,
  Detail,
  Details,
  Empty,
  Id,
  ObservedTime,
  PageHeader,
} from "./components";
import { DeploymentAction } from "./deployment-action";

export function ReleaseDetail({ id }: { id: string }) {
  const record = useSuspenseQuery(releaseQuery(id));
  const release = record.data.release;
  if (!release)
    return (
      <Card>
        <Empty title="Release not found">
          <Link to="/projects" search={{ cursor: "" }}>
            Back to projects
          </Link>
        </Empty>
      </Card>
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
  const current = environment?.currentReleaseId === release.id;
  const previous = environment?.previousReleaseId === release.id;
  const shortSha = build?.sourceSha.slice(0, 7);

  const content = (trigger?: ReactNode, panel?: ReactNode) => (
    <>
      <PageHeader
        breadcrumbs={[
          <Link
            key="environment"
            to="/environments/$environmentId"
            params={{ environmentId }}
            search={{ services: "", releases: "" }}
          >
            {environment?.settings?.name ?? "Environment"}
          </Link>,
        ]}
        title={
          <span>
            Release{" "}
            <code style={{ fontSize: "inherit" }}>{shortSha ?? ""}</code>
          </span>
        }
        badge={
          current ? (
            <Badge tone="success">Current</Badge>
          ) : previous ? (
            <Badge>Previous</Badge>
          ) : undefined
        }
        description={
          <ObservedTime value={release.createdAt} prefix="Created" />
        }
      >
        {!current && trigger}
      </PageHeader>
      <div className="stack">
        {panel}
        {(refreshFailed || environmentRecord.error) && (
          <Banner tone="warning">
            Couldn’t refresh this release. Details may be out of date.
          </Banner>
        )}
        <div className="grid-2">
          <div className="stack">
            <Card title="Images" labelledBy="release-images-title" flush>
              {release.images.length ? (
                <ul className="list">
                  {release.images.map((image) => (
                    <li key={image.service}>
                      <div className="list-row">
                        <span className="empty-icon" style={{ margin: 0 }}>
                          <PackageIcon size={18} aria-hidden="true" />
                        </span>
                        <span className="list-main">
                          <span className="list-title">{image.service}</span>
                          <span className="list-meta">
                            <Id value={image.imageDigest} label="image" />
                          </span>
                        </span>
                      </div>
                    </li>
                  ))}
                </ul>
              ) : (
                <Empty title="No images recorded" />
              )}
            </Card>
            <Card title="Domains" labelledBy="release-domains-title" flush>
              {release.domains.length ? (
                <ul className="list">
                  {release.domains.map((domain) => (
                    <li key={domain.hostname}>
                      <div className="list-row">
                        <span className="list-main">
                          <span className="list-title">{domain.hostname}</span>
                        </span>
                        <span className="chip">
                          {domain.service}:{domain.port}
                        </span>
                      </div>
                    </li>
                  ))}
                </ul>
              ) : (
                <Empty title="No public domains" />
              )}
            </Card>
          </div>
          <Card title="Details" labelledBy="release-details-title">
            <Details stacked>
              <Detail label="Commit">
                <Id value={build?.sourceSha ?? ""} label="commit" />
              </Detail>
              <Detail label="Branch">{build?.branch}</Detail>
              <Detail label="Settings version">
                {release.environmentVersion.toString()}
              </Detail>
              <Detail label="Secrets version">
                <Id
                  value={release.secretVersionId}
                  short
                  label="secrets version"
                />
              </Detail>
              <Detail label="Configuration hash">
                <Id
                  value={release.configurationSha256}
                  short
                  label="configuration hash"
                />
              </Detail>
            </Details>
          </Card>
        </div>
      </div>
    </>
  );

  if (!environment) return content();
  return (
    <DeploymentAction
      key={release.id}
      environment={environment}
      release={release}
      refreshFailed={refreshFailed || !!environmentRecord.error}
    >
      {({ trigger, panel }) => content(trigger, panel)}
    </DeploymentAction>
  );
}
