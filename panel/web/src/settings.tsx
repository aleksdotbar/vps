import { GithubLogoIcon } from "@phosphor-icons/react";
import { useSuspenseQuery } from "@tanstack/react-query";

import { integrationQuery } from "./api";
import { CloudflareConnection } from "./cloudflare-connection";
import {
  Badge,
  Banner,
  Card,
  Detail,
  Details,
  ButtonLink,
  PageHeader,
  RefreshButton,
} from "./components";
import { GitHubSetup } from "./github-setup";

export function Settings() {
  const { data, error, refetch } = useSuspenseQuery(integrationQuery());
  return (
    <>
      <PageHeader
        title="Settings"
        description="Connections this panel uses to build and publish your apps."
      />
      <div className="stack" style={{ maxWidth: "48rem" }}>
        <Card
          title={
            <span className="row" id="github">
              <GithubLogoIcon size={18} aria-hidden="true" />
              GitHub
              {data.installed ? (
                <Badge tone="success">Connected</Badge>
              ) : data.registered ? (
                <Badge tone="warning">Not installed</Badge>
              ) : (
                <Badge>Not connected</Badge>
              )}
            </span>
          }
          description="Reads Compose files from the repositories you choose."
          labelledBy="github-title"
          actions={
            <RefreshButton
              label="Refresh GitHub status"
              onRefresh={() => refetch()}
            />
          }
        >
          {error && (
            <Banner tone="warning">
              Couldn’t refresh GitHub status. Details may be out of date.
            </Banner>
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
                <ButtonLink variant="secondary" to="/settings/repositories">
                  Choose repositories
                </ButtonLink>
              )
            }
          />
        </Card>
        <CloudflareConnection />
      </div>
    </>
  );
}
