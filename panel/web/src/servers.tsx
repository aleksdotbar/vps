import {
  CaretRightIcon,
  HardDrivesIcon,
  PulseIcon,
} from "@phosphor-icons/react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { serverQuery, serversQuery } from "./api";
import {
  Badge,
  Banner,
  Card,
  Detail,
  Details,
  Empty,
  Id,
  ButtonLink,
  ObservedTime,
  PageHeader,
  Readiness,
} from "./components";
import { ServerTunnel } from "./server-tunnel";

export function Servers({ cursor }: { cursor: string }) {
  const { data, error } = useSuspenseQuery({
    ...serversQuery(cursor),
    refetchInterval: 30_000,
  });
  return (
    <>
      <PageHeader
        title="Servers"
        description="Hosts managed privately over Tailscale."
      />
      <div className="stack">
        {error && (
          <Banner tone="warning">
            Couldn’t refresh servers. Status may be out of date.
          </Banner>
        )}
        <section className="card" aria-label="Servers">
          {data.servers.length ? (
            <ul className="list">
              {data.servers.map((server) => (
                <li key={server.id}>
                  <Link
                    className="list-row"
                    to="/servers/$serverId"
                    params={{ serverId: server.id }}
                  >
                    <span className="empty-icon" style={{ margin: 0 }}>
                      <HardDrivesIcon size={18} aria-hidden="true" />
                    </span>
                    <span className="list-main">
                      <span className="list-title">
                        {server.name}
                        {server.panelHost && (
                          <Badge tone="info" plain>
                            Panel host
                          </Badge>
                        )}
                      </span>
                      <span className="list-meta">
                        <code>{server.tailscaleAddress}</code>
                        <ObservedTime
                          value={server.observedAt}
                          prefix="Checked"
                        />
                      </span>
                    </span>
                    <span className="list-aside">
                      <Readiness value={server.readiness} />
                      <CaretRightIcon className="chevron" size={16} />
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          ) : (
            <Empty icon={<HardDrivesIcon size={20} />} title="No servers yet">
              The panel’s own host appears here after <code>panel setup</code>.
            </Empty>
          )}
          {(cursor || data.page?.nextPageToken) && (
            <nav className="pagination" aria-label="Server pages">
              {cursor && (
                <ButtonLink
                  variant="secondary"
                  size="sm"
                  to="/servers"
                  search={{ cursor: "" }}
                >
                  Newest
                </ButtonLink>
              )}
              {data.page?.nextPageToken && (
                <ButtonLink
                  variant="secondary"
                  size="sm"
                  to="/servers"
                  search={{ cursor: data.page.nextPageToken }}
                >
                  Older
                </ButtonLink>
              )}
            </nav>
          )}
        </section>
      </div>
    </>
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
      <Card>
        <Empty title="Server not found">
          <Link to="/servers" search={{ cursor: "" }}>
            Back to servers
          </Link>
        </Empty>
      </Card>
    );

  return (
    <>
      <PageHeader
        breadcrumbs={[
          <Link key="servers" to="/servers" search={{ cursor: "" }}>
            Servers
          </Link>,
        ]}
        title={server.name}
        badge={<Readiness value={server.readiness} />}
        description={
          server.panelHost
            ? "Runs the panel itself, so it can’t be removed."
            : "Managed over private SSH through Tailscale."
        }
      >
        <ButtonLink
          variant="secondary"
          to="/operations"
          search={{ environment: "", server: id, cursor: "" }}
        >
          <PulseIcon size={16} aria-hidden="true" />
          Activity
        </ButtonLink>
      </PageHeader>
      <div className="stack">
        {error && (
          <Banner tone="warning">
            Couldn’t refresh this server. Status may be out of date.
          </Banner>
        )}
        <div className="grid-2">
          <ServerTunnel key={server.id} server={server} />
          <Card title="Details" labelledBy="server-details-title">
            <Details stacked>
              <Detail label="Tailscale address">
                <Id value={server.tailscaleAddress} label="address" />
              </Detail>
              <Detail label="Last checked">
                <ObservedTime value={server.observedAt} />
              </Detail>
              <Detail label="SSH host key">
                <Id value={server.sshHostKeyFingerprint} label="fingerprint" />
              </Detail>
              <Detail label="Server ID">
                <Id value={server.id} label="server ID" />
              </Detail>
            </Details>
          </Card>
        </div>
      </div>
    </>
  );
}
