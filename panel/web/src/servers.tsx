import {
  Badge,
  Banner,
  Empty,
  Grid,
  LayerCard,
  Pagination,
  Text,
} from "@cloudflare/kumo";
import { HardDrivesIcon, PulseIcon, WarningIcon } from "@phosphor-icons/react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";

import { serverQuery, serversQuery } from "./api";
import {
  ButtonLink,
  cardLink,
  cardTitleLink,
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
        <Grid variant="3up" gap="base">
          {data.servers.map((server) => (
            <LayerCard key={server.id} className={cardLink}>
              <LayerCard.Secondary className="flex items-center justify-between gap-2">
                <span className="flex min-w-0 items-center gap-2">
                  <HardDrivesIcon size={18} aria-hidden="true" />
                  <Text variant="heading" truncate>
                    <Link
                      className={cardTitleLink}
                      to="/servers/$serverId"
                      params={{ serverId: server.id }}
                    >
                      {server.name}
                    </Link>
                  </Text>
                </span>
                <Readiness value={server.readiness} />
              </LayerCard.Secondary>
              <LayerCard.Primary className="flex-1 flex flex-col gap-2">
                <Text variant="mono-secondary">{server.tailscaleAddress}</Text>
                <Text variant="secondary" size="sm">
                  <ObservedTime value={server.observedAt} prefix="Checked" />
                </Text>
                {server.panelHost && (
                  <span>
                    <Badge variant="info">Panel host</Badge>
                  </span>
                )}
              </LayerCard.Primary>
            </LayerCard>
          ))}
        </Grid>
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
