import {
  Badge,
  Banner,
  Empty,
  Grid,
  LayerCard,
  Table,
  Text,
} from "@cloudflare/kumo";
import { WarningIcon } from "@phosphor-icons/react";
import { useSuspenseQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import type { Release } from "../../gen/ts/panel/v1/panel_pb";
import { environmentQuery, releaseQuery } from "./api";
import {
  ButtonLink,
  Detail,
  Details,
  Id,
  ObservedTime,
  Page,
} from "./components";
import { DeploymentAction } from "./deployment-action";

export function ReleaseDetail({ id }: { id: string }) {
  const record = useSuspenseQuery(releaseQuery(id));
  const release = record.data.release;
  if (!release)
    return (
      <Page title="Release not found">
        <Empty
          title="Release not found"
          contents={
            <ButtonLink to="/projects" search={{ cursor: "" }}>
              Back to projects
            </ButtonLink>
          }
        />
      </Page>
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
    <Page
      title={`Release ${shortSha ?? ""}`.trim()}
      crumbs={[
        {
          label: environment?.settings?.name ?? "Environment",
          href: `/environments/${environmentId}`,
        },
      ]}
      actions={
        <>
          {current ? (
            <Badge variant="success">Current</Badge>
          ) : (
            previous && <Badge variant="neutral">Previous</Badge>
          )}
          <Text as="span" variant="secondary">
            <ObservedTime value={release.createdAt} prefix="Created" />
          </Text>
          {!current && trigger}
        </>
      }
    >
      {panel}
      {(refreshFailed || environmentRecord.error) && (
        <Banner
          variant="alert"
          icon={<WarningIcon weight="fill" />}
          description="Couldn’t refresh this release. Details may be out of date."
        />
      )}
      <Grid variant="2-1" gap="base">
        <div className="flex flex-col gap-6">
          <LayerCard>
            <LayerCard.Secondary>Images</LayerCard.Secondary>
            <LayerCard.Primary>
              {release.images.length ? (
                <Table>
                  <Table.Header>
                    <Table.Row>
                      <Table.Head>Service</Table.Head>
                      <Table.Head>Image</Table.Head>
                    </Table.Row>
                  </Table.Header>
                  <Table.Body>
                    {release.images.map((image) => (
                      <Table.Row key={image.service}>
                        <Table.Cell>{image.service}</Table.Cell>
                        <Table.Cell>
                          <Id value={image.imageDigest} />
                        </Table.Cell>
                      </Table.Row>
                    ))}
                  </Table.Body>
                </Table>
              ) : (
                <Empty size="sm" title="No images recorded" />
              )}
            </LayerCard.Primary>
          </LayerCard>
          <LayerCard>
            <LayerCard.Secondary>Domains</LayerCard.Secondary>
            <LayerCard.Primary>
              {release.domains.length ? (
                <Table>
                  <Table.Header>
                    <Table.Row>
                      <Table.Head>Hostname</Table.Head>
                      <Table.Head>Service</Table.Head>
                    </Table.Row>
                  </Table.Header>
                  <Table.Body>
                    {release.domains.map((domain) => (
                      <Table.Row key={domain.hostname}>
                        <Table.Cell>{domain.hostname}</Table.Cell>
                        <Table.Cell>
                          <Badge variant="outline">
                            {domain.service}:{domain.port}
                          </Badge>
                        </Table.Cell>
                      </Table.Row>
                    ))}
                  </Table.Body>
                </Table>
              ) : (
                <Empty size="sm" title="No public domains" />
              )}
            </LayerCard.Primary>
          </LayerCard>
        </div>
        <LayerCard>
          <LayerCard.Secondary>Details</LayerCard.Secondary>
          <LayerCard.Primary>
            <Details>
              <Detail label="Commit">
                <Id value={build?.sourceSha ?? ""} />
              </Detail>
              <Detail label="Branch">{build?.branch}</Detail>
              <Detail label="Settings version">
                {release.environmentVersion.toString()}
              </Detail>
              <Detail label="Secrets version">
                <Id value={release.secretVersionId} short />
              </Detail>
              <Detail label="Configuration hash">
                <Id value={release.configurationSha256} short />
              </Detail>
            </Details>
          </LayerCard.Primary>
        </LayerCard>
      </Grid>
    </Page>
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
