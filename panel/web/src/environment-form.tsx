import { create } from "@bufbuild/protobuf";
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
import { Banner, Button, ButtonLink } from "./components";
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
      setNotice("Unsaved changes discarded.");
    } catch (error) {
      if (!isCurrent()) return;
      if (!(await expireSession(error)) && isCurrent())
        setFailure("Couldn’t reload the settings. Try again.");
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
        setNotice("Saved. Deploy to apply the changes.");
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
            "Saved, but the page couldn’t reload. Refresh it before editing again.",
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
          "These settings were changed elsewhere. Showing the latest version — review and save again.",
        );
        try {
          await reload(isCurrent);
        } catch (refreshError) {
          if (!isCurrent()) return;
          if (!(await expireSession(refreshError)) && isCurrent())
            setFailure(
              "These settings were changed elsewhere. Reload the page.",
            );
        }
      } else if (mutationRejected(error)) {
        setIntent(undefined);
        onLockChange?.(false);
        setFailure(
          error instanceof ConnectError && error.code === Code.AlreadyExists
            ? "That name or one of the domains is already in use."
            : "These settings were refused. Check the server, branch, override path and domains.",
        );
      } else {
        setFailure(
          "We couldn’t confirm the save. Check again — it won’t apply twice.",
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
      setFailure("The override path is too long.");
      return;
    }
    if (
      new Set(settings.domains.map((domain) => domain.hostname)).size !==
      settings.domains.length
    ) {
      setFailure("Each domain can only be used once.");
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
      {stale && !intent && (
        <Banner
          tone="warning"
          actions={
            <Button size="sm" pending={pending} onClick={() => void refresh()}>
              Load latest
            </Button>
          }
        >
          These settings changed since you opened this page.
        </Banner>
      )}
      {serverPage.error && (
        <Banner tone="warning">
          Couldn’t load servers. Reload the page before saving.
        </Banner>
      )}
      {!serverPage.data.servers.length && !selectedServer && (
        <Banner tone="info">
          No servers are registered yet.{" "}
          <Link to="/servers" search={{ cursor: "" }}>
            Open servers
          </Link>
        </Banner>
      )}
      <form className="form" onSubmit={submit} aria-busy={pending}>
        <fieldset disabled={blocked} className="stack">
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
            serverPages={
              !pending &&
              !intent &&
              !createdId &&
              (cursor || serverPage.data.page?.nextPageToken) ? (
                <span className="row">
                  {cursor &&
                    (environment ? (
                      <ButtonLink
                        variant="secondary"
                        size="sm"
                        to="/environments/$environmentId/settings"
                        params={{ environmentId: environment.id }}
                        search={{ servers: "" }}
                      >
                        First servers
                      </ButtonLink>
                    ) : (
                      <ButtonLink
                        variant="secondary"
                        size="sm"
                        to="/projects/$projectId/environments/new"
                        params={{ projectId }}
                        search={{ preset, servers: "" }}
                      >
                        First servers
                      </ButtonLink>
                    ))}
                  {serverPage.data.page?.nextPageToken &&
                    (environment ? (
                      <ButtonLink
                        variant="secondary"
                        size="sm"
                        to="/environments/$environmentId/settings"
                        params={{ environmentId: environment.id }}
                        search={{ servers: serverPage.data.page.nextPageToken }}
                      >
                        More servers
                      </ButtonLink>
                    ) : (
                      <ButtonLink
                        variant="secondary"
                        size="sm"
                        to="/projects/$projectId/environments/new"
                        params={{ projectId }}
                        search={{
                          preset,
                          servers: serverPage.data.page.nextPageToken,
                        }}
                      >
                        More servers
                      </ButtonLink>
                    ))}
                </span>
              ) : undefined
            }
          />
        </fieldset>
        {failure && (
          <p className="field-error" role="alert">
            {failure}
          </p>
        )}
        {notice && (
          <p className="help" role="status">
            {notice}
          </p>
        )}
        <div className="form-actions">
          {intent ? (
            <Button
              type="button"
              variant="primary"
              pending={pending}
              onClick={() => void save(intent)}
            >
              Check save
            </Button>
          ) : (
            <Button
              type="submit"
              variant="primary"
              pending={pending}
              disabled={blocked && !pending}
            >
              {environment ? "Save changes" : "Create environment"}
            </Button>
          )}
          {environment && (
            <Button
              type="button"
              variant="ghost"
              disabled={pending || !!intent}
              onClick={() => void refresh()}
            >
              Discard changes
            </Button>
          )}
          {createdId && (
            <Link
              to="/environments/$environmentId"
              params={{ environmentId: createdId }}
              search={{ services: "", releases: "" }}
            >
              Open environment
            </Link>
          )}
        </div>
      </form>
    </>
  );
}
