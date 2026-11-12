import { useSuspenseQuery } from "@tanstack/react-query";
import { getRouteApi, useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { Alert, AlertTitle } from "@/components/ui/alert";
import {
  Empty,
  EmptyContent,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";

import type { Server } from "../../gen/ts/panel/v1/panel_pb";
import { environmentQuery, projectQuery } from "./api";
import { ButtonLink, EnvironmentStatus, Page } from "./components";
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

function NotFound({ title }: { title: string }) {
  return (
    <Page title={title}>
      <Empty>
        <EmptyHeader>
          <EmptyTitle>{title}</EmptyTitle>
        </EmptyHeader>
        <EmptyContent>
          <ButtonLink to="/projects">Back to projects</ButtonLink>
        </EmptyContent>
      </Empty>
    </Page>
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
  const navigate = useNavigate();
  const [locked, setLocked] = useState(false);
  if (!data.project) return <NotFound title="Project not found" />;
  return (
    <Page
      title="New environment"
      description="Deploy one branch to a server, with its own secrets and domains."
      crumbs={[
        {
          label: "Projects",
          link: { to: "/projects" },
        },
        {
          label: data.project.name,
          link: {
            to: "/projects/$projectId",
            params: { projectId },
          },
        },
      ]}
      actions={
        !locked && (
          <Tabs
            value={preset}
            onValueChange={(value) =>
              void navigate({
                to: "/projects/$projectId/environments/new",
                params: { projectId },
                search: {
                  preset: value === "production" ? "production" : "staging",
                },
              })
            }
          >
            <TabsList aria-label="Start from">
              <TabsTrigger value="staging">Staging</TabsTrigger>
              <TabsTrigger value="production">Production</TabsTrigger>
            </TabsList>
          </Tabs>
        )
      }
    >
      <div className="flex max-w-3xl flex-col gap-6">
        {error && (
          <Alert>
            <AlertTitle>
              Couldn’t refresh the project. Details may be out of date.
            </AlertTitle>
          </Alert>
        )}
        <EnvironmentForm
          key={`${projectId}:${preset}`}
          projectId={projectId}
          preset={preset}
          cursor={cursor}
          onLockChange={setLocked}
        />
      </div>
    </Page>
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
  if (!environment?.settings) return <NotFound title="Environment not found" />;
  return (
    <Page
      title="Settings"
      description="Changes apply on the next deployment."
      crumbs={[
        {
          label: environment.settings.name,
          link: {
            to: "/environments/$environmentId",
            params: { environmentId: id },
          },
        },
      ]}
      actions={<EnvironmentStatus environment={environment} />}
    >
      <div className="flex max-w-3xl flex-col gap-6">
        {error && (
          <Alert>
            <AlertTitle>
              Couldn’t refresh these settings. Reload before saving.
            </AlertTitle>
          </Alert>
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
    </Page>
  );
}
