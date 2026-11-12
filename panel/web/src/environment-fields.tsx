import { create } from "@bufbuild/protobuf";
import { PlusIcon, Trash2Icon } from "lucide-react";
import type { ReactNode } from "react";

import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

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
  const items = options.map((server) => ({
    value: server.id,
    label: `${server.name} — ${server.tailscaleAddress}`,
  }));
  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>General</CardTitle>
        </CardHeader>
        <CardContent>
          <FieldGroup>
            <div className="grid gap-6 sm:grid-cols-2">
              <Field>
                <FieldLabel htmlFor="environment-name">Name</FieldLabel>
                <Input
                  id="environment-name"
                  value={settings.name}
                  onChange={(event) => field("name", event.target.value)}
                  required
                  maxLength={63}
                  pattern={"[a-z][a-z0-9\\-]*"}
                />
                <FieldDescription>
                  Lowercase letters, digits and dashes.
                </FieldDescription>
              </Field>
              <Field>
                <FieldLabel htmlFor="environment-branch">Branch</FieldLabel>
                <Input
                  id="environment-branch"
                  value={settings.branch}
                  onChange={(event) => field("branch", event.target.value)}
                  required
                  maxLength={255}
                  pattern={"[A-Za-z0-9][A-Za-z0-9._\\/\\-]*"}
                />
              </Field>
            </div>
            <Field>
              <FieldLabel htmlFor="environment-server">Server</FieldLabel>
              <div className="flex items-center gap-2">
                <Select
                  items={items}
                  value={settings.serverId || null}
                  disabled={disabled || serverFixed}
                  required
                  onValueChange={(value) => {
                    const server = options.find((entry) => entry.id === value);
                    if (server) {
                      field("serverId", server.id);
                      onServerChange(server);
                    }
                  }}
                >
                  <SelectTrigger
                    id="environment-server"
                    className="min-w-0 flex-1"
                  >
                    <SelectValue placeholder="Choose a server…" />
                  </SelectTrigger>
                  <SelectContent>
                    {items.map((item) => (
                      <SelectItem key={item.value} value={item.value}>
                        {item.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {selectedServer && (
                  <Readiness value={selectedServer.readiness} />
                )}
              </div>
              <FieldDescription>
                {serverFixed
                  ? "Fixed after the first deployment."
                  : "Can’t be changed once a deployment has started."}
              </FieldDescription>
              {serverPages}
            </Field>
            <Field>
              <FieldLabel htmlFor="environment-override">
                Compose override file
              </FieldLabel>
              <Input
                id="environment-override"
                value={settings.composeOverridePath}
                onChange={(event) =>
                  field("composeOverridePath", event.target.value)
                }
                required
                maxLength={512}
                className="font-mono"
              />
              <FieldDescription>
                Applied on top of <code>compose.yaml</code>, e.g.{" "}
                <code>compose.production.yaml</code>.
              </FieldDescription>
            </Field>
            <Field orientation="horizontal">
              <Checkbox
                id="environment-auto-deploy"
                checked={settings.autoDeploy}
                disabled={disabled}
                onCheckedChange={(checked) => field("autoDeploy", checked)}
              />
              <FieldContent>
                <FieldLabel htmlFor="environment-auto-deploy">
                  Deploy automatically
                </FieldLabel>
                <FieldDescription>
                  Every push to <code>{settings.branch || "the branch"}</code>{" "}
                  deploys once its images are built.
                </FieldDescription>
              </FieldContent>
            </Field>
          </FieldGroup>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>Domains</CardTitle>
          <CardDescription>
            Public hostnames routed to a service. Services without a domain stay
            private.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {settings.domains.map((entry, index) => (
            <div
              className="flex flex-wrap items-end gap-2"
              key={index}
              role="group"
              aria-label={`Domain ${index + 1}`}
            >
              <Field className="min-w-48 flex-2">
                <FieldLabel htmlFor={`domain-host-${index}`}>
                  Hostname
                </FieldLabel>
                <Input
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
              </Field>
              <Field className="min-w-32 flex-1">
                <FieldLabel htmlFor={`domain-service-${index}`}>
                  Service
                </FieldLabel>
                <Input
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
              </Field>
              <Field className="w-24">
                <FieldLabel htmlFor={`domain-port-${index}`}>Port</FieldLabel>
                <Input
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
              </Field>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={`Remove domain ${index + 1}`}
                onClick={() =>
                  field(
                    "domains",
                    settings.domains.filter(
                      (_, position) => position !== index,
                    ),
                  )
                }
              >
                <Trash2Icon />
              </Button>
            </div>
          ))}
          <div>
            <Button
              type="button"
              variant="outline"
              disabled={settings.domains.length >= 32}
              onClick={() =>
                field("domains", [...settings.domains, create(DomainSchema)])
              }
            >
              <PlusIcon data-icon="inline-start" />
              Add domain
            </Button>
          </div>
        </CardContent>
      </Card>
    </>
  );
}
