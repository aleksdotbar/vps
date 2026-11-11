import { create } from "@bufbuild/protobuf";
import { Button } from "@cloudflare/kumo/components/button";

import { DomainSchema } from "../../gen/ts/panel/v1/panel_pb";
import type {
  EnvironmentSettings,
  Server,
} from "../../gen/ts/panel/v1/panel_pb";
import { ObservedTime, Readiness } from "./components";

export function EnvironmentFields({
  settings,
  onChange,
  servers,
  selectedServer,
  onServerChange,
  serverFixed,
}: {
  settings: EnvironmentSettings;
  onChange: (value: EnvironmentSettings) => void;
  servers: Server[];
  selectedServer?: Server;
  onServerChange: (value: Server) => void;
  serverFixed: boolean;
}) {
  function field<K extends keyof EnvironmentSettings>(
    key: K,
    value: EnvironmentSettings[K],
  ) {
    onChange({ ...settings, [key]: value });
  }
  const options =
    selectedServer && !servers.some((server) => server.id === selectedServer.id)
      ? [selectedServer, ...servers]
      : servers;
  return (
    <>
      <label htmlFor="environment-name">Environment name</label>
      <input
        id="environment-name"
        value={settings.name}
        onChange={(event) => field("name", event.target.value)}
        required
        maxLength={63}
        pattern={"[a-z][a-z0-9\\-]*"}
      />
      <label htmlFor="environment-server">Server</label>
      <select
        id="environment-server"
        value={settings.serverId}
        disabled={serverFixed}
        required
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
        <option value="">Choose a registered server</option>
        {options.map((server) => (
          <option key={server.id} value={server.id}>
            {server.name} ({server.tailscaleAddress})
          </option>
        ))}
      </select>
      {selectedServer && (
        <p>
          <Readiness value={selectedServer.readiness} />{" "}
          <span className="muted">
            Observed <ObservedTime value={selectedServer.observedAt} />
          </span>
        </p>
      )}
      <p className="muted">
        Readiness is the last observation, not a deployment guarantee.{" "}
        {serverFixed
          ? "An environment with release history cannot move to another server."
          : "The backend also rejects a move after execution has started."}
      </p>
      <label htmlFor="environment-branch">Branch</label>
      <input
        id="environment-branch"
        value={settings.branch}
        onChange={(event) => field("branch", event.target.value)}
        required
        maxLength={255}
        pattern={"[A-Za-z0-9][A-Za-z0-9._\\/\\-]*"}
      />
      <label htmlFor="environment-override">Compose override path</label>
      <input
        id="environment-override"
        value={settings.composeOverridePath}
        onChange={(event) => field("composeOverridePath", event.target.value)}
        required
        maxLength={512}
        aria-describedby="override-help"
      />
      <p id="override-help" className="muted">
        Use a normalized relative .yaml or .yml path inside the repository.
        compose.yaml is the base file; it cannot be its own override.
      </p>
      <label className="checkbox-label">
        <input
          type="checkbox"
          checked={settings.autoDeploy}
          onChange={(event) => field("autoDeploy", event.target.checked)}
        />
        <span>Deploy automatically from this branch</span>
      </label>
      <p className="muted">
        Enabling automation allows eligible branch builds to queue deployments.
        Disabling it cancels unstarted automatic work; started work still
        finishes or reconciles.
      </p>
      <h2>Domains</h2>
      <p className="muted">
        Routes apply on a future deployment. The target service must exist in
        that release. Services without a route stay private.
      </p>
      {settings.domains.map((domain, index) => (
        <fieldset className="domain-row" key={index}>
          <legend>Domain {index + 1}</legend>
          <label htmlFor={`domain-host-${index}`}>Hostname</label>
          <input
            id={`domain-host-${index}`}
            value={domain.hostname}
            required
            maxLength={253}
            pattern={
              "([a-z0-9]([a-z0-9\\-]{0,61}[a-z0-9])?\\.)+[a-z0-9]([a-z0-9\\-]{0,61}[a-z0-9])?"
            }
            onChange={(event) =>
              field(
                "domains",
                settings.domains.map((entry, position) =>
                  position === index
                    ? { ...entry, hostname: event.target.value }
                    : entry,
                ),
              )
            }
          />
          <label htmlFor={`domain-service-${index}`}>Compose service</label>
          <input
            id={`domain-service-${index}`}
            value={domain.service}
            required
            maxLength={128}
            pattern={"[A-Za-z0-9._\\-]+"}
            onChange={(event) =>
              field(
                "domains",
                settings.domains.map((entry, position) =>
                  position === index
                    ? { ...entry, service: event.target.value }
                    : entry,
                ),
              )
            }
          />
          <label htmlFor={`domain-port-${index}`}>Internal port</label>
          <input
            id={`domain-port-${index}`}
            type="number"
            value={domain.port || ""}
            required
            min={1}
            max={65535}
            step={1}
            onChange={(event) =>
              field(
                "domains",
                settings.domains.map((entry, position) =>
                  position === index
                    ? { ...entry, port: Number(event.target.value) }
                    : entry,
                ),
              )
            }
          />
          <Button
            type="button"
            variant="secondary"
            onClick={() =>
              field(
                "domains",
                settings.domains.filter((_, position) => position !== index),
              )
            }
          >
            Remove domain {index + 1}
          </Button>
        </fieldset>
      ))}
      <Button
        type="button"
        disabled={settings.domains.length >= 32}
        onClick={() =>
          field("domains", [...settings.domains, create(DomainSchema)])
        }
      >
        Add domain ({settings.domains.length}/32)
      </Button>
    </>
  );
}
