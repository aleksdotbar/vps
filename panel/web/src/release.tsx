import { useSuspenseQuery } from "@tanstack/react-query";
import { BoxIcon, GlobeIcon, TriangleAlertIcon } from "lucide-react";
import { Fragment, type ReactNode } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty";
import {
  Item,
  ItemActions,
  ItemContent,
  ItemGroup,
  ItemMedia,
  ItemSeparator,
  ItemTitle,
} from "@/components/ui/item";

import type { Release } from "../../gen/ts/panel/v1/panel_pb";
import { environmentQuery, releaseQuery } from "./api";
import {
  ButtonLink,
  Detail,
  Details,
  Id,
  ObservedTime,
  Page,
  StatusBadge,
} from "./components";
import { DeploymentAction } from "./deployment-action";

export function ReleaseDetail({ id }: { id: string }) {
  const record = useSuspenseQuery(releaseQuery(id));
  const release = record.data.release;
  if (!release)
    return (
      <Page title="Release not found">
        <Empty className="border">
          <EmptyHeader>
            <EmptyTitle>Release not found</EmptyTitle>
          </EmptyHeader>
          <EmptyContent>
            <ButtonLink variant="outline" to="/projects">
              Back to projects
            </ButtonLink>
          </EmptyContent>
        </Empty>
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
          link: {
            to: "/environments/$environmentId",
            params: { environmentId },
          },
        },
      ]}
      actions={
        <>
          {current ? (
            <StatusBadge tone="success">Current</StatusBadge>
          ) : (
            previous && <StatusBadge tone="neutral">Previous</StatusBadge>
          )}
          <span className="self-center text-sm text-muted-foreground">
            <ObservedTime value={release.createdAt} prefix="Created" />
          </span>
          {!current && trigger}
        </>
      }
    >
      {panel}
      {(refreshFailed || environmentRecord.error) && (
        <Alert>
          <TriangleAlertIcon />
          <AlertDescription>
            Couldn’t refresh this release. Details may be out of date.
          </AlertDescription>
        </Alert>
      )}
      <div className="grid gap-6 lg:grid-cols-3">
        <div className="flex flex-col gap-6 lg:col-span-2">
          <Card>
            <CardHeader>
              <CardTitle>Images</CardTitle>
            </CardHeader>
            <CardContent>
              {release.images.length ? (
                <ItemGroup>
                  {release.images.map((image, index) => (
                    <Fragment key={image.service}>
                      {index > 0 && <ItemSeparator className="my-0" />}
                      <Item size="sm" className="px-0">
                        <ItemMedia variant="icon">
                          <BoxIcon className="text-muted-foreground" />
                        </ItemMedia>
                        <ItemContent>
                          <ItemTitle>{image.service}</ItemTitle>
                        </ItemContent>
                        <ItemActions>
                          <Id value={image.imageDigest} short />
                        </ItemActions>
                      </Item>
                    </Fragment>
                  ))}
                </ItemGroup>
              ) : (
                <p className="text-sm text-muted-foreground">
                  No images recorded.
                </p>
              )}
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>Domains</CardTitle>
            </CardHeader>
            <CardContent>
              {release.domains.length ? (
                <ItemGroup>
                  {release.domains.map((domain, index) => (
                    <Fragment key={domain.hostname}>
                      {index > 0 && <ItemSeparator className="my-0" />}
                      <Item size="sm" className="px-0">
                        <ItemMedia variant="icon">
                          <GlobeIcon className="text-muted-foreground" />
                        </ItemMedia>
                        <ItemContent className="min-w-0">
                          <ItemTitle>
                            <a
                              href={`https://${domain.hostname}`}
                              target="_blank"
                              rel="noreferrer"
                              className="truncate underline-offset-4 hover:underline"
                            >
                              {domain.hostname}
                            </a>
                          </ItemTitle>
                        </ItemContent>
                        <ItemActions className="text-muted-foreground">
                          {domain.service}:{domain.port}
                        </ItemActions>
                      </Item>
                    </Fragment>
                  ))}
                </ItemGroup>
              ) : (
                <p className="text-sm text-muted-foreground">
                  No public domains.
                </p>
              )}
            </CardContent>
          </Card>
        </div>
        <Card className="self-start">
          <CardHeader>
            <CardTitle>Details</CardTitle>
          </CardHeader>
          <CardContent>
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
          </CardContent>
        </Card>
      </div>
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
