import { create } from "@bufbuild/protobuf";
import {
  Button,
  Checkbox,
  Grid,
  Input,
  LayerCard,
  Select,
  Text,
} from "@cloudflare/kumo";
import { PlusIcon, TrashIcon } from "@phosphor-icons/react";
import type { ReactNode } from "react";

import { DomainSchema } from "../../gen/ts/panel/v1/panel_pb";
import type {
  EnvironmentSettings,
  Server,
} from "../../gen/ts/panel/v1/panel_pb";
import { Readiness } from "./components";

export function EnvironmentFields({
  settings,
  onChange,
  servers,
  selectedServer,
  onServerChange,
  serverFixed,
  serverPages,
  disabled = false,
}: {
  settings: EnvironmentSettings;
  onChange: (value: EnvironmentSettings) => void;
  servers: Server[];
  selectedServer?: Server;
  onServerChange: (value: Server) => void;
  serverFixed: boolean;
  serverPages?: ReactNode;
  disabled?: boolean;
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
      <LayerCard>
        <LayerCard.Secondary>General</LayerCard.Secondary>
        <LayerCard.Primary className="flex flex-col gap-4">
          <Grid variant="2up" gap="sm">
            <Input
              id="environment-name"
              label="Name"
              description="Lowercase letters, digits and dashes."
              value={settings.name}
              onChange={(event) => field("name", event.target.value)}
              required
              maxLength={63}
              pattern={"[a-z][a-z0-9\\-]*"}
            />
            <Input
              id="environment-branch"
              label="Branch"
              value={settings.branch}
              onChange={(event) => field("branch", event.target.value)}
              required
              maxLength={255}
              pattern={"[A-Za-z0-9][A-Za-z0-9._\\/\\-]*"}
            />
          </Grid>
          <div className="flex flex-col gap-2">
            <div className="flex items-end gap-2">
              <Select
                className="min-w-0 flex-1"
                label="Server"
                description={
                  serverFixed
                    ? "Fixed after the first deployment."
                    : "Can’t be changed once a deployment has started."
                }
                placeholder="Choose a server…"
                required
                disabled={disabled || serverFixed}
                value={settings.serverId || null}
                items={options.map((server) => ({
                  value: server.id,
                  label: `${server.name} — ${server.tailscaleAddress}`,
                }))}
                onValueChange={(value) => {
                  const server = options.find((entry) => entry.id === value);
                  if (server) {
                    field("serverId", server.id);
                    onServerChange(server);
                  }
                }}
              />
              {selectedServer && <Readiness value={selectedServer.readiness} />}
            </div>
            {serverPages}
          </div>
          <Input
            id="environment-override"
            label="Compose override file"
            description={
              <>
                Applied on top of <code>compose.yaml</code>, e.g.{" "}
                <code>compose.production.yaml</code>.
              </>
            }
            value={settings.composeOverridePath}
            onChange={(event) =>
              field("composeOverridePath", event.target.value)
            }
            required
            maxLength={512}
            className="font-mono"
          />
          <div className="flex flex-col gap-1">
            <Checkbox
              label="Deploy automatically"
              checked={settings.autoDeploy}
              disabled={disabled}
              onCheckedChange={(checked) => field("autoDeploy", checked)}
            />
            <Text variant="secondary" size="sm">
              Every push to <code>{settings.branch || "the branch"}</code>{" "}
              deploys once its images are built.
            </Text>
          </div>
        </LayerCard.Primary>
      </LayerCard>
      <LayerCard>
        <LayerCard.Secondary>Domains</LayerCard.Secondary>
        <LayerCard.Primary className="flex flex-col gap-4">
          <Text variant="secondary">
            Public hostnames routed to a service. Services without a domain stay
            private.
          </Text>
          {settings.domains.map((entry, index) => (
            <div
              className="flex flex-wrap items-end gap-2"
              key={index}
              role="group"
              aria-label={`Domain ${index + 1}`}
            >
              <Input
                id={`domain-host-${index}`}
                label="Hostname"
                className="min-w-48 flex-2"
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
              <Input
                id={`domain-service-${index}`}
                label="Service"
                className="min-w-32 flex-1"
                value={entry.service}
                required
                maxLength={128}
                placeholder="web"
                pattern={"[A-Za-z0-9._\\-]+"}
                onChange={(event) =>
                  domain(index, { service: event.target.value })
                }
              />
              <Input
                id={`domain-port-${index}`}
                label="Port"
                className="w-24"
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
              <Button
                type="button"
                variant="ghost"
                shape="square"
                aria-label={`Remove domain ${index + 1}`}
                icon={<TrashIcon size={16} />}
                onClick={() =>
                  field(
                    "domains",
                    settings.domains.filter(
                      (_, position) => position !== index,
                    ),
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
        </LayerCard.Primary>
      </LayerCard>
    </>
  );
}
