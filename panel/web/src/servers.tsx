import {
  Badge,
  Banner,
  Empty,
  Grid,
  LayerCard,
  Pagination,
  Table,
} from "@cloudflare/kumo";
import { HardDrivesIcon, PulseIcon, WarningIcon } from "@phosphor-icons/react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";

import { serverQuery, serversQuery } from "./api";
import {
  ButtonLink,
  Detail,
  Details,
  Id,
  ObservedTime,
  Page,
  Readiness,
} from "./components";
import { ServerTunnel } from "./server-tunnel";

export function Servers({ cursor }: { cursor: string }) {
  const navigate = useNavigate();
  const { data, error } = useSuspenseQuery({
    ...serversQuery(cursor),
    refetchInterval: 30_000,
  });
  const next = data.page?.nextPageToken ?? "";
  return (
    <Page title="Servers" description="Hosts managed privately over Tailscale.">
      {error && (
        <Banner
          variant="alert"
          icon={<WarningIcon weight="fill" />}
          description="Couldn’t refresh servers. Status may be out of date."
        />
      )}
      {data.servers.length ? (
        <LayerCard>
          <LayerCard.Primary className="p-0">
            <Table aria-label="Servers">
              <Table.Header>
                <Table.Row>
                  <Table.Head>Name</Table.Head>
                  <Table.Head>Tailscale address</Table.Head>
                  <Table.Head>Last checked</Table.Head>
                  <Table.Head>Status</Table.Head>
                </Table.Row>
              </Table.Header>
              <Table.Body>
                {data.servers.map((server) => (
                  <Table.Row key={server.id}>
                    <Table.Cell>
                      <span className="flex items-center gap-2">
                        <Link
                          to="/servers/$serverId"
                          params={{ serverId: server.id }}
                          className="font-medium"
                        >
                          {server.name}
                        </Link>
                        {server.panelHost && (
                          <Badge variant="info">Panel host</Badge>
                        )}
                      </span>
                    </Table.Cell>
                    <Table.Cell>
                      <Id value={server.tailscaleAddress} />
                    </Table.Cell>
                    <Table.Cell>
                      <ObservedTime value={server.observedAt} />
                    </Table.Cell>
                    <Table.Cell>
                      <Readiness value={server.readiness} />
                    </Table.Cell>
                  </Table.Row>
                ))}
              </Table.Body>
            </Table>
          </LayerCard.Primary>
        </LayerCard>
      ) : (
        <Empty
          icon={<HardDrivesIcon size={48} />}
          title="No servers yet"
          description="The panel’s own host appears here after panel setup."
        />
      )}
      {(cursor || next) && (
        <Pagination
          controls="simple"
          page={cursor ? 2 : 1}
          hasNextPage={!!next}
          // The cursor API can't step back, so Previous returns to the first page.
          setPage={(page) =>
            void navigate({
              to: "/servers",
              search: { cursor: page > (cursor ? 2 : 1) ? next : "" },
            })
          }
        />
      )}
    </Page>
  );
}

export function Server({ id }: { id: string }) {
  const { data, error } = useSuspenseQuery({
    ...serverQuery(id),
    refetchInterval: 30_000,
  });
  const server = data.server;
  if (!server)
    return (
      <Page
        title="Server not found"
        crumbs={[{ label: "Servers", href: "/servers" }]}
      >
        <Empty
          title="Server not found"
          contents={
            <ButtonLink to="/servers" search={{ cursor: "" }}>
              Back to servers
            </ButtonLink>
          }
        />
      </Page>
    );

  return (
    <Page
      title={server.name}
      crumbs={[{ label: "Servers", href: "/servers" }]}
      description={
        server.panelHost
          ? "Runs the panel itself, so it can’t be removed."
          : "Managed over private SSH through Tailscale."
      }
      actions={
        <ButtonLink
          variant="secondary"
          icon={<PulseIcon size={16} />}
          to="/operations"
          search={{ environment: "", server: id, cursor: "" }}
        >
          Activity
        </ButtonLink>
      }
    >
      {error && (
        <Banner
          variant="alert"
          icon={<WarningIcon weight="fill" />}
          description="Couldn’t refresh this server. Status may be out of date."
        />
      )}
      <Grid variant="2up" gap="base">
        <ServerTunnel key={server.id} server={server} />
        <LayerCard>
          <LayerCard.Secondary className="flex items-center justify-between gap-2">
            Details
            <Readiness value={server.readiness} />
          </LayerCard.Secondary>
          <LayerCard.Primary>
            <Details>
              <Detail label="Tailscale address">
                <Id value={server.tailscaleAddress} />
              </Detail>
              <Detail label="Last checked">
                <ObservedTime value={server.observedAt} />
              </Detail>
              <Detail label="SSH host key">
                <Id value={server.sshHostKeyFingerprint} />
              </Detail>
              <Detail label="Server ID">
                <Id value={server.id} />
              </Detail>
            </Details>
          </LayerCard.Primary>
        </LayerCard>
      </Grid>
    </Page>
  );
}
