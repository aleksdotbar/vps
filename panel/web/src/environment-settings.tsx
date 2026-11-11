import { useSuspenseQuery } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { useState } from "react";

import type { Server } from "../../gen/ts/panel/v1/panel_pb";
import { environmentQuery, projectQuery } from "./api";
import { Empty, PageTitle } from "./components";
import { EnvironmentForm } from "./environment-form";

const newEnvironmentRoute = getRouteApi(
  "/_private/projects/$projectId/environments/new",
);
const settingsRoute = getRouteApi(
  "/_private/environments/$environmentId/settings",
);

export function NewEnvironmentRoute() {
  const search = newEnvironmentRoute.useSearch();
  return (
    <CreateEnvironment
      projectId={newEnvironmentRoute.useParams().projectId}
      preset={search.preset}
      cursor={search.servers}
    />
  );
}

export function EnvironmentSettingsRoute() {
  return (
    <EditEnvironment
      id={settingsRoute.useParams().environmentId}
      cursor={settingsRoute.useSearch().servers}
      initialServer={settingsRoute.useLoaderData()}
    />
  );
}

export function CreateEnvironment({
  projectId,
  preset,
  cursor,
}: {
  projectId: string;
  preset: "staging" | "production";
  cursor: string;
}) {
  const { data, error } = useSuspenseQuery(projectQuery(projectId));
  const [locked, setLocked] = useState(false);
  if (!data.project)
    return (
      <Empty title="Project unavailable">Return to Projects and refresh.</Empty>
    );
  return (
    <>
      <Link
        className="back-link"
        to="/projects/$projectId"
        params={{ projectId }}
        search={{ cursor: "" }}
      >
        Project environments
      </Link>
      <PageTitle title="New environment" description={data.project.name} />
      {error && (
        <p role="alert">
          Project refresh failed. Previously loaded project details may be out
          of date.
        </p>
      )}
      {!locked && (
        <nav className="form-actions" aria-label="Environment defaults">
          <Link
            to="/projects/$projectId/environments/new"
            params={{ projectId }}
            search={{ preset: "staging", servers: "" }}
            aria-current={preset === "staging" ? "page" : undefined}
          >
            Start with staging defaults
          </Link>
          <Link
            to="/projects/$projectId/environments/new"
            params={{ projectId }}
            search={{ preset: "production", servers: "" }}
            aria-current={preset === "production" ? "page" : undefined}
          >
            Start with production defaults
          </Link>
        </nav>
      )}
      <EnvironmentForm
        key={`${projectId}:${preset}`}
        projectId={projectId}
        preset={preset}
        cursor={cursor}
        onLockChange={setLocked}
      />
    </>
  );
}

export function EditEnvironment({
  id,
  cursor,
  initialServer,
}: {
  id: string;
  cursor: string;
  initialServer?: Server;
}) {
  const { data, error } = useSuspenseQuery(environmentQuery(id));
  const environment = data.environment;
  if (!environment?.settings)
    return (
      <Empty title="Environment unavailable">
        Return to the project and refresh.
      </Empty>
    );
  return (
    <>
      <Link
        className="back-link"
        to="/environments/$environmentId"
        params={{ environmentId: id }}
        search={{ services: "", releases: "" }}
      >
        Environment details
      </Link>
      <PageTitle
        title={`${environment.settings.name} settings`}
        description={`Current version: ${environment.version}`}
      >
        <span className="status">
          {environment.pendingChanges
            ? "Changes pending"
            : environment.currentReleaseId
              ? "Applied"
              : "Not deployed"}
        </span>
      </PageTitle>
      {error && (
        <p role="alert">
          Environment refresh failed. Refresh current settings before saving.
        </p>
      )}
      <EnvironmentForm
        key={id}
        projectId={environment.projectId}
        environment={environment}
        preset="staging"
        cursor={cursor}
        initialServer={initialServer}
        metadataError={!!error}
      />
    </>
  );
}
