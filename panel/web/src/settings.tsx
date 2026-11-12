import {
  Badge,
  Banner,
  LayerCard,
  RefreshButton,
  Text,
} from "@cloudflare/kumo";
import { GithubLogoIcon, WarningIcon } from "@phosphor-icons/react";
import { useMutation, useSuspenseQuery } from "@tanstack/react-query";

import { integrationQuery } from "./api";
import { CloudflareConnection } from "./cloudflare-connection";
import { ButtonLink, Detail, Details, Page } from "./components";
import { GitHubSetup } from "./github-setup";

export function Settings() {
  const { data, error, refetch } = useSuspenseQuery(integrationQuery());
  const refresh = useMutation({ mutationFn: () => refetch() });
  return (
    <Page
      title="Settings"
      description="Connections this panel uses to build and publish your apps."
    >
      <div className="flex max-w-3xl flex-col gap-6">
        <LayerCard id="github">
          <LayerCard.Secondary className="flex items-center justify-between gap-2">
            <span className="flex items-center gap-2">
              <GithubLogoIcon size={18} aria-hidden="true" />
              GitHub
              {data.installed ? (
                <Badge variant="success">Connected</Badge>
              ) : data.registered ? (
                <Badge variant="warning">Not installed</Badge>
              ) : (
                <Badge variant="neutral">Not connected</Badge>
              )}
            </span>
            <RefreshButton
              aria-label="Refresh GitHub status"
              loading={refresh.isPending}
              onClick={() => refresh.mutate()}
            />
          </LayerCard.Secondary>
          <LayerCard.Primary className="flex flex-col gap-4">
            <Text variant="secondary">
              Reads Compose files from the repositories you choose.
            </Text>
            {error && (
              <Banner
                variant="alert"
                icon={<WarningIcon weight="fill" />}
                description="Couldn’t refresh GitHub status. Details may be out of date."
              />
            )}
            {data.registered && (
              <Details>
                <Detail label="Owner">{data.owner || "—"}</Detail>
                <Detail label="App">
                  {data.installed ? "Installed" : "Created, not installed yet"}
                </Detail>
              </Details>
            )}
            <GitHubSetup
              registered={data.registered}
              installed={data.installed}
              actions={
                data.installed && (
                  <ButtonLink to="/settings/repositories">
                    Choose repositories
                  </ButtonLink>
                )
              }
            />
          </LayerCard.Primary>
        </LayerCard>
        <CloudflareConnection />
      </div>
    </Page>
  );
}
