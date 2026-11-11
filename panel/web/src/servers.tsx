import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { serverQuery, serversQuery } from "./api";
import {
  Detail,
  Details,
  Empty,
  ObservedTime,
  PageTitle,
  Readiness,
} from "./components";
import { ServerTunnel } from "./server-tunnel";

export function Servers({ cursor }: { cursor: string }) {
  const { data, error, isFetching } = useSuspenseQuery({
    ...serversQuery(cursor),
    refetchInterval: 30_000,
  });
  return (
    <>
      <PageTitle
        title="Servers"
        description="Private hosts managed by this installation."
      />
      {error && (
        <p role="alert">Refresh failed. Server status may be out of date.</p>
      )}
      <section aria-label="Servers" aria-busy={isFetching}>
        {data.servers.length ? (
          <ul className="resource-list">
            {data.servers.map((server) => (
              <li key={server.id}>
                <div>
                  <Link
                    className="resource-name"
                    to="/servers/$serverId"
                    params={{ serverId: server.id }}
                  >
                    {server.name}
                  </Link>
                  <p>
                    {server.tailscaleAddress}
                    {server.panelHost && (
                      <span className="inline-note">Panel host</span>
                    )}
                  </p>
                </div>
                <div className="resource-meta">
                  <Readiness value={server.readiness} />
                  <span className="muted">
                    Observed <ObservedTime value={server.observedAt} />
                  </span>
                </div>
              </li>
            ))}
          </ul>
        ) : (
          <Empty title="No servers available">
            Registered servers will appear here after setup.
          </Empty>
        )}
      </section>
      <nav className="pagination" aria-label="Server pages">
        {cursor && (
          <Link to="/servers" search={{ cursor: "" }}>
            Newest servers
          </Link>
        )}
        {data.page?.nextPageToken && (
          <Link to="/servers" search={{ cursor: data.page.nextPageToken }}>
            Older servers
          </Link>
        )}
      </nav>
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
      <Empty title="Server unavailable">
        Return to servers and refresh the list.
      </Empty>
    );

  return (
    <>
      <Link className="back-link" to="/servers" search={{ cursor: "" }}>
        All servers
      </Link>
      <PageTitle
        title={server.name}
        description={
          server.panelHost
            ? "This server hosts the panel and cannot be removed."
            : "Managed through private SSH over Tailscale."
        }
      >
        <Readiness value={server.readiness} />
      </PageTitle>
      {error && (
        <p role="alert">Refresh failed. Server status may be out of date.</p>
      )}
      <Details>
        <Detail label="Tailscale address">
          <code>{server.tailscaleAddress}</code>
        </Detail>
        <Detail label="SSH host fingerprint">
          <code>{server.sshHostKeyFingerprint}</code>
        </Detail>
        <Detail label="Last observation">
          <ObservedTime value={server.observedAt} />
        </Detail>
        <Detail label="Server ID">
          <code>{server.id}</code>
        </Detail>
      </Details>
      <p className="muted">
        Readiness records the last server observation. It does not establish
        current application health.
      </p>
      <p className="action-link">
        <Link
          to="/operations"
          search={{ environment: "", server: id, cursor: "" }}
        >
          Server operation history
        </Link>
      </p>
      <ServerTunnel key={server.id} server={server} />
    </>
  );
}
