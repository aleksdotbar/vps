import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  Banner,
  Button,
  Pagination,
  useKumoToastManager,
} from "@cloudflare/kumo";
import { useMutation, useSuspenseQuery } from "@tanstack/react-query";
import { Link, useNavigate, useRouter } from "@tanstack/react-router";
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
  const { capturePage } = usePageContinuation();
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
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const toast = useKumoToastManager();
  const fail = (title: string) => toast.add({ title, variant: "error" });
  const [createdId, setCreatedId] = useState("");
  const stale = !!environment && environment.version !== baseVersion;
  const refreshing = useMutation({ mutationFn: refresh });
  const saving = useMutation({ mutationFn: save });
  const pending = refreshing.isPending || saving.isPending;
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
    if (intent || router.state.status === "pending") return;
    const isCurrent = capturePage();
    try {
      await reload(isCurrent);
    } catch (error) {
      if (!isCurrent()) return;
      if (!(await expireSession(error)) && isCurrent())
        fail("Couldn’t reload the settings. Try again.");
    }
  }

  async function save(next: Intent) {
    if (createdId || router.state.status === "pending") return;
    const isCurrent = capturePage();
    onLockChange?.(true);
    setIntent(next);
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
        if (next.kind === "create") setCreatedId(savedId);
        else {
          setNeedsRefresh(true);
          toast.add({
            title: "Saved. Deploy to apply the changes.",
            variant: "success",
          });
        }
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
          fail(
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
        fail(
          "These settings were changed elsewhere. Showing the latest version — review and save again.",
        );
        try {
          await reload(isCurrent);
        } catch (refreshError) {
          if (!isCurrent()) return;
          if (!(await expireSession(refreshError)) && isCurrent())
            fail("These settings were changed elsewhere. Reload the page.");
        }
      } else if (mutationRejected(error)) {
        setIntent(undefined);
        onLockChange?.(false);
        fail(
          error instanceof ConnectError && error.code === Code.AlreadyExists
            ? "That name or one of the domains is already in use."
            : "These settings were refused. Check the server, branch, override path and domains.",
        );
      } else {
        fail(
          "We couldn’t confirm the save. Check again — it won’t apply twice.",
        );
      }
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (blocked) return;
    if (new TextEncoder().encode(settings.composeOverridePath).length > 512) {
      fail("The override path is too long.");
      return;
    }
    if (
      new Set(settings.domains.map((domain) => domain.hostname)).size !==
      settings.domains.length
    ) {
      fail("Each domain can only be used once.");
      return;
    }
    const mutation = { requestId: crypto.randomUUID() };
    saving.mutate(
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
          variant="alert"
          title="These settings changed since you opened this page."
          action={
            <Button
              size="sm"
              loading={refreshing.isPending}
              disabled={pending}
              onClick={() => refreshing.mutate()}
            >
              Load latest
            </Button>
          }
        />
      )}
      {serverPage.error && (
        <Banner
          variant="alert"
          title="Couldn’t load servers. Reload the page before saving."
        />
      )}
      {!serverPage.data.servers.length && !selectedServer && (
        <Banner
          title="No servers are registered yet."
          action={
            <Link to="/servers" search={{ cursor: "" }}>
              Open servers
            </Link>
          }
        />
      )}
      <form
        className="flex flex-col gap-6"
        onSubmit={submit}
        aria-busy={pending}
      >
        <fieldset disabled={blocked} className="flex flex-col gap-6">
          <EnvironmentFields
            disabled={blocked}
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
                <ServerPages
                  environmentId={environment?.id}
                  projectId={projectId}
                  preset={preset}
                  cursor={cursor}
                  next={serverPage.data.page?.nextPageToken ?? ""}
                />
              ) : undefined
            }
          />
        </fieldset>
        <div className="flex flex-wrap items-center gap-2">
          {intent ? (
            <Button
              type="button"
              variant="primary"
              loading={saving.isPending}
              onClick={() => saving.mutate(intent)}
            >
              Check save
            </Button>
          ) : (
            <Button
              type="submit"
              variant="primary"
              loading={saving.isPending}
              disabled={blocked}
            >
              {environment ? "Save changes" : "Create environment"}
            </Button>
          )}
          {environment && (
            <Button
              type="button"
              variant="ghost"
              loading={refreshing.isPending}
              disabled={pending || !!intent}
              onClick={() => refreshing.mutate()}
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

function ServerPages({
  environmentId,
  projectId,
  preset,
  cursor,
  next,
}: {
  environmentId?: string;
  projectId: string;
  preset: "staging" | "production";
  cursor: string;
  next: string;
}) {
  const navigate = useNavigate();
  return (
    <Pagination
      controls="simple"
      page={cursor ? 2 : 1}
      hasNextPage={!!next}
      setPage={(page) => {
        // Cursor paging can't step back, so Previous returns to the first page.
        const servers = page > (cursor ? 2 : 1) ? next : "";
        void (environmentId
          ? navigate({
              to: "/environments/$environmentId/settings",
              params: { environmentId },
              search: { servers },
            })
          : navigate({
              to: "/projects/$projectId/environments/new",
              params: { projectId },
              search: { preset, servers },
            }));
      }}
    />
  );
}
