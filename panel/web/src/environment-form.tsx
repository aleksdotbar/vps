import { create } from "@bufbuild/protobuf";
import { Button } from "@cloudflare/kumo/components/button";
import { Code, ConnectError } from "@connectrpc/connect";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link, useRouter } from "@tanstack/react-router";
import { useState } from "react";
import type { FormEvent } from "react";
import { z } from "zod";

import { EnvironmentSettingsSchema } from "../../gen/ts/panel/v1/panel_pb";
import type { Environment, Server } from "../../gen/ts/panel/v1/panel_pb";
import {
  environmentListKey,
  mutationRejected,
  panel,
  queryClient,
  refreshEnvironment,
  serverQuery,
  serversQuery,
} from "./api";
import { EnvironmentFields } from "./environment-fields";
import { usePageContinuation } from "./use-page-continuation";
import { useSessionExpiry } from "./use-session-expiry";

type Intent =
  | { kind: "create"; request: Parameters<typeof panel.createEnvironment>[0] }
  | { kind: "update"; request: Parameters<typeof panel.updateEnvironment>[0] };

export function EnvironmentForm({
  projectId,
  environment,
  preset,
  cursor,
  initialServer,
  metadataError = false,
  onLockChange,
}: {
  projectId: string;
  environment?: Environment;
  preset: "staging" | "production";
  cursor: string;
  initialServer?: Server;
  metadataError?: boolean;
  onLockChange?: (value: boolean) => void;
}) {
  const router = useRouter();
  const { capturePage, isMounted } = usePageContinuation();
  const expireSession = useSessionExpiry();
  const serverPage = useSuspenseQuery(serversQuery(cursor));
  const [settings, setSettings] = useState(
    () =>
      environment?.settings ??
      create(EnvironmentSettingsSchema, {
        name: preset,
        branch: preset === "staging" ? "main" : "prod",
        composeOverridePath: `compose.${preset}.yaml`,
      }),
  );
  const [baseVersion, setBaseVersion] = useState(environment?.version ?? 0n);
  const [selectedServer, setSelectedServer] = useState(initialServer);
  const [intent, setIntent] = useState<Intent>();
  const [pending, setPending] = useState(false);
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [failure, setFailure] = useState("");
  const [notice, setNotice] = useState("");
  const [createdId, setCreatedId] = useState("");
  const stale = !!environment && environment.version !== baseVersion;
  const blocked =
    pending ||
    !!intent ||
    needsRefresh ||
    stale ||
    metadataError ||
    !!serverPage.error ||
    !!createdId;

  async function reload(isCurrent: () => boolean) {
    if (!environment) return;
    const current = await refreshEnvironment(environment.id);
    if (!isCurrent()) return;
    const server = await queryClient.fetchQuery({
      ...serverQuery(current.settings!.serverId),
      staleTime: 0,
    });
    if (!isCurrent()) return;
    setSettings(current.settings!);
    setBaseVersion(current.version);
    setSelectedServer(server.server);
    setNeedsRefresh(false);
  }

  async function refresh() {
    if (pending || intent || router.state.status === "pending") return;
    const isCurrent = capturePage();
    setPending(true);
    setFailure("");
    setNotice("");
    try {
      await reload(isCurrent);
      if (!isCurrent()) return;
      setNotice("Current settings loaded. Unsaved edits were discarded.");
    } catch (error) {
      if (!isCurrent()) return;
      if (!(await expireSession(error)) && isCurrent())
        setFailure("Settings refresh failed. Refresh before saving again.");
    } finally {
      if (isMounted()) setPending(false);
    }
  }

  async function save(next: Intent) {
    if (pending || createdId || router.state.status === "pending") return;
    const isCurrent = capturePage();
    setPending(true);
    onLockChange?.(true);
    setIntent(next);
    setFailure("");
    setNotice("");
    try {
      const result =
        next.kind === "create"
          ? await panel.createEnvironment(next.request)
          : await panel.updateEnvironment(next.request);
      if (!result.environment?.settings)
        throw new Error("Missing saved environment");
      const savedId = z.uuid().parse(result.environment.id);
      if (isCurrent()) {
        setIntent(undefined);
        setNotice(
          `Environment version ${result.environment.version} saved. Running applications were not restarted.`,
        );
        if (next.kind === "create") setCreatedId(savedId);
        else setNeedsRefresh(true);
      }
      try {
        await queryClient.invalidateQueries({
          queryKey: environmentListKey(projectId),
        });
        if (!isCurrent()) return;
        if (next.kind === "create")
          await router.navigate({
            to: "/environments/$environmentId",
            params: { environmentId: savedId },
            search: { services: "", releases: "" },
          });
        else await reload(isCurrent);
      } catch (refreshError) {
        if (!isCurrent()) return;
        if (!(await expireSession(refreshError)) && isCurrent())
          setFailure(
            "Saved, but current details could not be loaded. Open the environment or refresh before editing again.",
          );
      }
    } catch (error) {
      if (!isCurrent()) return;
      if (await expireSession(error)) return;
      if (!isCurrent()) return;
      if (
        error instanceof ConnectError &&
        error.code === Code.Aborted &&
        environment
      ) {
        setIntent(undefined);
        onLockChange?.(false);
        setNeedsRefresh(true);
        setFailure(
          "Settings changed elsewhere. Refreshing the current version discards this draft; review before saving again.",
        );
        try {
          await reload(isCurrent);
        } catch (refreshError) {
          if (!isCurrent()) return;
          if (!(await expireSession(refreshError)) && isCurrent())
            setFailure(
              "Settings changed elsewhere and refresh failed. Refresh before saving again.",
            );
        }
      } else if (mutationRejected(error)) {
        setIntent(undefined);
        onLockChange?.(false);
        setFailure(
          error instanceof ConnectError && error.code === Code.AlreadyExists
            ? "The environment name or a hostname is already in use. Check the name and domain ownership."
            : "Settings were refused. Check the server, branch, relative override path and domains. A server move is refused after deployment execution starts.",
        );
      } else {
        setFailure(
          "Save outcome unknown. Check the same request to recover its result without repeating the change.",
        );
      }
    } finally {
      if (isMounted()) setPending(false);
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (blocked) return;
    if (new TextEncoder().encode(settings.composeOverridePath).length > 512) {
      setFailure("The Compose override path must not exceed 512 bytes.");
      return;
    }
    if (
      new Set(settings.domains.map((domain) => domain.hostname)).size !==
      settings.domains.length
    ) {
      setFailure("Each hostname may appear only once.");
      return;
    }
    const mutation = { requestId: crypto.randomUUID() };
    void save(
      environment
        ? {
            kind: "update",
            request: {
              mutation,
              id: environment.id,
              expectedVersion: baseVersion,
              settings,
            },
          }
        : { kind: "create", request: { mutation, projectId, settings } },
    );
  }

  return (
    <>
      <p className="notice">
        Saving settings creates pending configuration. Domain edits leave active
        routes and already queued release snapshots unchanged. Server, branch or
        override changes invalidate unstarted work for the previous binding.
      </p>
      {stale && !intent && (
        <p role="alert">
          Settings or secrets changed since this form opened. Refresh current
          settings before saving.
        </p>
      )}
      {serverPage.error && (
        <p role="alert">
          Server refresh failed. Reload the server list before saving.
        </p>
      )}
      {!serverPage.data.servers.length && !selectedServer && (
        <p role="status">
          No registered servers on this page. Check{" "}
          <Link to="/servers" search={{ cursor: "" }}>
            Servers
          </Link>{" "}
          before creating an environment.
        </p>
      )}
      <form
        className="settings-form environment-form"
        onSubmit={submit}
        aria-busy={pending}
      >
        <fieldset disabled={blocked} className="form-fields">
          <EnvironmentFields
            settings={settings}
            onChange={setSettings}
            servers={serverPage.data.servers}
            selectedServer={
              serverPage.data.servers.find(
                (server) => server.id === settings.serverId,
              ) ?? selectedServer
            }
            onServerChange={setSelectedServer}
            serverFixed={
              !!environment?.currentReleaseId ||
              !!environment?.previousReleaseId
            }
          />
        </fieldset>
        {failure && (
          <p className="form-error" role="alert">
            {failure}
          </p>
        )}
        {notice && <p role="status">{notice}</p>}
        {createdId && (
          <Link
            to="/environments/$environmentId"
            params={{ environmentId: createdId }}
            search={{ services: "", releases: "" }}
          >
            Open created environment
          </Link>
        )}
        <div className="form-actions">
          {intent ? (
            <Button
              type="button"
              disabled={pending}
              onClick={() => void save(intent)}
            >
              Check save result
            </Button>
          ) : (
            <Button type="submit" variant="primary" disabled={blocked}>
              {environment ? "Save settings" : "Create environment"}
            </Button>
          )}
          {environment && (
            <Button
              type="button"
              variant="secondary"
              disabled={pending || !!intent}
              onClick={() => void refresh()}
            >
              Discard edits and refresh
            </Button>
          )}
          <Button
            type="button"
            variant="secondary"
            disabled={pending || !!intent || serverPage.isFetching}
            onClick={() => void serverPage.refetch()}
          >
            Refresh server choices
          </Button>
        </div>
      </form>
      {!pending && !intent && !createdId && (
        <nav className="pagination" aria-label="Server choices">
          {cursor &&
            (environment ? (
              <Link
                to="/environments/$environmentId/settings"
                params={{ environmentId: environment.id }}
                search={{ servers: "" }}
              >
                Newest servers
              </Link>
            ) : (
              <Link
                to="/projects/$projectId/environments/new"
                params={{ projectId }}
                search={{ preset, servers: "" }}
              >
                Newest servers
              </Link>
            ))}
          {serverPage.data.page?.nextPageToken &&
            (environment ? (
              <Link
                to="/environments/$environmentId/settings"
                params={{ environmentId: environment.id }}
                search={{ servers: serverPage.data.page.nextPageToken }}
              >
                Older servers
              </Link>
            ) : (
              <Link
                to="/projects/$projectId/environments/new"
                params={{ projectId }}
                search={{ preset, servers: serverPage.data.page.nextPageToken }}
              >
                Older servers
              </Link>
            ))}
        </nav>
      )}
    </>
  );
}
