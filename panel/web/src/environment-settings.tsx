import { useSuspenseQuery } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { useState } from "react";

import type { Server } from "../../gen/ts/panel/v1/panel_pb";
import { environmentQuery, projectQuery } from "./api";
import {
  Banner,
  Card,
  Empty,
  EnvironmentStatus,
  PageHeader,
} from "./components";
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
      <Card>
        <Empty title="Project not found">
          <Link to="/projects" search={{ cursor: "" }}>
            Back to projects
          </Link>
        </Empty>
      </Card>
    );
  return (
    <>
      <PageHeader
        breadcrumbs={[
          <Link key="projects" to="/projects" search={{ cursor: "" }}>
            Projects
          </Link>,
          <Link
            key="project"
            to="/projects/$projectId"
            params={{ projectId }}
            search={{ cursor: "" }}
          >
            {data.project.name}
          </Link>,
        ]}
        title="New environment"
        description="Deploy one branch to a server, with its own secrets and domains."
      >
        {!locked && (
          <nav className="segmented" aria-label="Start from">
            <Link
              to="/projects/$projectId/environments/new"
              params={{ projectId }}
              search={{ preset: "staging", servers: "" }}
              aria-current={preset === "staging" ? "page" : undefined}
            >
              Staging
            </Link>
            <Link
              to="/projects/$projectId/environments/new"
              params={{ projectId }}
              search={{ preset: "production", servers: "" }}
              aria-current={preset === "production" ? "page" : undefined}
            >
              Production
            </Link>
          </nav>
        )}
      </PageHeader>
      <div className="stack" style={{ maxWidth: "48rem" }}>
        {error && (
          <Banner tone="warning">
            Couldn’t refresh the project. Details may be out of date.
          </Banner>
        )}
        <EnvironmentForm
          key={`${projectId}:${preset}`}
          projectId={projectId}
          preset={preset}
          cursor={cursor}
          onLockChange={setLocked}
        />
      </div>
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
      <Card>
        <Empty title="Environment not found">
          <Link to="/projects" search={{ cursor: "" }}>
            Back to projects
          </Link>
        </Empty>
      </Card>
    );
  return (
    <>
      <PageHeader
        breadcrumbs={[
          <Link
            key="environment"
            to="/environments/$environmentId"
            params={{ environmentId: id }}
            search={{ services: "", releases: "" }}
          >
            {environment.settings.name}
          </Link>,
        ]}
        title="Settings"
        badge={<EnvironmentStatus environment={environment} />}
        description="Changes apply on the next deployment."
      />
      <div className="stack" style={{ maxWidth: "48rem" }}>
        {error && (
          <Banner tone="warning">
            Couldn’t refresh these settings. Reload before saving.
          </Banner>
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
      </div>
    </>
  );
}
