import { create } from "@bufbuild/protobuf";
import { PlusIcon, TrashIcon } from "@phosphor-icons/react";
import type { ReactNode } from "react";

import { DomainSchema } from "../../gen/ts/panel/v1/panel_pb";
import type {
  EnvironmentSettings,
  Server,
} from "../../gen/ts/panel/v1/panel_pb";
import { Button, Card, Readiness } from "./components";

export function EnvironmentFields({
  settings,
  onChange,
  servers,
  selectedServer,
  onServerChange,
  serverFixed,
  serverPages,
}: {
  settings: EnvironmentSettings;
  onChange: (value: EnvironmentSettings) => void;
  servers: Server[];
  selectedServer?: Server;
  onServerChange: (value: Server) => void;
  serverFixed: boolean;
  serverPages?: ReactNode;
}) {
  function field<K extends keyof EnvironmentSettings>(
    key: K,
    value: EnvironmentSettings[K],
  ) {
    onChange({ ...settings, [key]: value });
  }
  function domain(
    index: number,
    change: Partial<(typeof settings.domains)[number]>,
  ) {
    field(
      "domains",
      settings.domains.map((entry, position) =>
        position === index ? { ...entry, ...change } : entry,
      ),
    );
  }
  const options =
    selectedServer && !servers.some((server) => server.id === selectedServer.id)
      ? [selectedServer, ...servers]
      : servers;
  return (
    <>
      <Card title="General" labelledBy="general-title">
        <div className="fields">
          <div className="fields-2">
            <div className="field">
              <label htmlFor="environment-name">Name</label>
              <input
                id="environment-name"
                value={settings.name}
                onChange={(event) => field("name", event.target.value)}
                required
                maxLength={63}
                pattern={"[a-z][a-z0-9\\-]*"}
                aria-describedby="environment-name-help"
              />
              <p className="help" id="environment-name-help">
                Lowercase letters, digits and dashes.
              </p>
            </div>
            <div className="field">
              <label htmlFor="environment-branch">Branch</label>
              <input
                id="environment-branch"
                value={settings.branch}
                onChange={(event) => field("branch", event.target.value)}
                required
                maxLength={255}
                pattern={"[A-Za-z0-9][A-Za-z0-9._\\/\\-]*"}
              />
            </div>
          </div>
          <div className="field">
            <label htmlFor="environment-server">Server</label>
            <div className="row" style={{ flexWrap: "nowrap" }}>
              <select
                id="environment-server"
                value={settings.serverId}
                disabled={serverFixed}
                required
                aria-describedby="environment-server-help"
                onChange={(event) => {
                  const server = options.find(
                    (entry) => entry.id === event.target.value,
                  );
                  if (server) {
                    field("serverId", server.id);
                    onServerChange(server);
                  }
                }}
              >
                <option value="">Choose a server…</option>
                {options.map((server) => (
                  <option key={server.id} value={server.id}>
                    {server.name} — {server.tailscaleAddress}
                  </option>
                ))}
              </select>
              {selectedServer && <Readiness value={selectedServer.readiness} />}
            </div>
            <p className="help" id="environment-server-help">
              {serverFixed
                ? "Fixed after the first deployment."
                : "Can’t be changed once a deployment has started."}
            </p>
            {serverPages}
          </div>
          <div className="field">
            <label htmlFor="environment-override">Compose override file</label>
            <input
              id="environment-override"
              value={settings.composeOverridePath}
              onChange={(event) =>
                field("composeOverridePath", event.target.value)
              }
              required
              maxLength={512}
              className="mono"
              aria-describedby="override-help"
            />
            <p id="override-help" className="help">
              Applied on top of <code>compose.yaml</code>, e.g.{" "}
              <code>compose.production.yaml</code>.
            </p>
          </div>
          <label className="checkbox">
            <input
              type="checkbox"
              checked={settings.autoDeploy}
              onChange={(event) => field("autoDeploy", event.target.checked)}
            />
            <span className="checkbox-text">
              <span>Deploy automatically</span>
              <span className="help">
                Every push to <code>{settings.branch || "the branch"}</code>{" "}
                deploys once its images are built.
              </span>
            </span>
          </label>
        </div>
      </Card>
      <Card
        title="Domains"
        description="Public hostnames routed to a service. Services without a domain stay private."
        labelledBy="domains-title"
      >
        {settings.domains.map((entry, index) => (
          <div
            className="repeat-row"
            key={index}
            role="group"
            aria-label={`Domain ${index + 1}`}
          >
            <div className="field">
              <label htmlFor={`domain-host-${index}`}>Hostname</label>
              <input
                id={`domain-host-${index}`}
                value={entry.hostname}
                required
                maxLength={253}
                placeholder="app.example.com"
                pattern={
                  "([a-z0-9]([a-z0-9\\-]{0,61}[a-z0-9])?\\.)+[a-z0-9]([a-z0-9\\-]{0,61}[a-z0-9])?"
                }
                onChange={(event) =>
                  domain(index, { hostname: event.target.value })
                }
              />
            </div>
            <div className="field">
              <label htmlFor={`domain-service-${index}`}>Service</label>
              <input
                id={`domain-service-${index}`}
                value={entry.service}
                required
                maxLength={128}
                placeholder="web"
                pattern={"[A-Za-z0-9._\\-]+"}
                onChange={(event) =>
                  domain(index, { service: event.target.value })
                }
              />
            </div>
            <div className="field">
              <label htmlFor={`domain-port-${index}`}>Port</label>
              <input
                id={`domain-port-${index}`}
                type="number"
                value={entry.port || ""}
                required
                min={1}
                max={65535}
                step={1}
                placeholder="3000"
                onChange={(event) =>
                  domain(index, { port: Number(event.target.value) })
                }
              />
            </div>
            <Button
              type="button"
              variant="ghost"
              shape="square"
              aria-label={`Remove domain ${index + 1}`}
              icon={<TrashIcon size={16} />}
              onClick={() =>
                field(
                  "domains",
                  settings.domains.filter((_, position) => position !== index),
                )
              }
            />
          </div>
        ))}
        <div>
          <Button
            type="button"
            icon={<PlusIcon size={16} />}
            disabled={settings.domains.length >= 32}
            onClick={() =>
              field("domains", [...settings.domains, create(DomainSchema)])
            }
          >
            Add domain
          </Button>
        </div>
      </Card>
    </>
  );
}
