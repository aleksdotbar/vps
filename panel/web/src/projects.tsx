import { useQuery, useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  CircleCheckIcon,
  CircleIcon,
  FolderIcon,
  GitBranchIcon,
  GlobeIcon,
  PlusIcon,
  TriangleAlertIcon,
  FolderGit2Icon,
} from "lucide-react";
import type { ReactNode } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";

import { ServerTunnelPhase } from "../../gen/ts/panel/v1/cloudflare_pb";
import type {
  Environment as EnvironmentRecord,
  Project as ProjectRecord,
} from "../../gen/ts/panel/v1/panel_pb";
import {
  cloudflareIntegrationQuery,
  environmentsQuery,
  integrationQuery,
  projectQuery,
  projectsQuery,
  serversQuery,
  serverTunnelQuery,
} from "./api";
import {
  ButtonLink,
  type Crumb,
  EnvironmentStatus,
  ObservedTime,
  Page,
  StatusBadge,
  CursorPagination,
} from "./components";

// A clickable card: the title link's hit area stretches over the card, so badges stay outside the link.
const clickableCard =
  "relative transition-colors hover:bg-muted/50 has-[a:focus-visible]:ring-3 has-[a:focus-visible]:ring-ring/30";
const stretchedLink = "after:absolute after:inset-0 focus-visible:outline-none";

function StaleAlert({ children }: { children: ReactNode }) {
  return (
    <Alert>
      <TriangleAlertIcon />
      <AlertDescription>{children}</AlertDescription>
    </Alert>
  );
}

export function Projects({ cursor }: { cursor: string }) {
  const { data, error } = useSuspenseQuery(projectsQuery(cursor));
  return (
    <Page
      title="Projects"
      description="Apps deployed from your GitHub repositories."
      actions={
        <ButtonLink to="/projects/new" search={{ repository: "" }}>
          <PlusIcon data-icon="inline-start" />
          New project
        </ButtonLink>
      }
    >
      <SetupChecklist hasProject={data.projects.length > 0 || !!cursor} />
      {error && (
        <StaleAlert>
          Couldn’t refresh projects. Showing the last loaded list.
        </StaleAlert>
      )}
      {data.projects.length ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {data.projects.map((project) => (
            <ProjectCard key={project.id} project={project} />
          ))}
        </div>
      ) : (
        <Empty className="border">
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <FolderIcon />
            </EmptyMedia>
            <EmptyTitle>
              {cursor ? "No more projects" : "No projects yet"}
            </EmptyTitle>
            <EmptyDescription>
              A project connects one GitHub repository. Its environments decide
              which branch deploys where.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <ButtonLink to="/projects/new" search={{ repository: "" }}>
              New project
            </ButtonLink>
          </EmptyContent>
        </Empty>
      )}
      <CursorPagination
        previous={
          cursor ? { to: "/projects", search: { cursor: "" } } : undefined
        }
        next={
          data.page?.nextPageToken
            ? { to: "/projects", search: { cursor: data.page.nextPageToken } }
            : undefined
        }
      />
    </Page>
  );
}

function ProjectCard({ project }: { project: ProjectRecord }) {
  // shortcut: one environments request per card; fine for a handful of projects.
  const environments = useQuery(environmentsQuery(project.id)).data
    ?.environments;
  return (
    <Card className={clickableCard}>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <FolderIcon className="size-4 text-muted-foreground" />
          <Link
            className={stretchedLink}
            to="/projects/$projectId"
            params={{ projectId: project.id }}
            search={{ cursor: "" }}
          >
            {project.name}
          </Link>
        </CardTitle>
        <CardDescription className="flex items-center gap-1.5">
          <FolderGit2Icon />
          <span className="truncate">
            {project.repository
              ? `${project.repository.owner}/${project.repository.name}`
              : "Repository unavailable"}
          </span>
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {!!environments?.length && (
          <div className="flex flex-wrap gap-1.5">
            {environments.map((environment) => (
              <StatusBadge
                key={environment.id}
                tone={
                  environment.pendingChanges
                    ? "warning"
                    : environment.currentReleaseId
                      ? "success"
                      : "neutral"
                }
              >
                {environment.settings?.name ?? "Environment"}
              </StatusBadge>
            ))}
          </div>
        )}
        <span className="text-muted-foreground">
          <ObservedTime value={project.createdAt} prefix="Created" />
        </span>
      </CardContent>
    </Card>
  );
}

function EnvironmentCard({
  environment,
  serverName,
}: {
  environment: EnvironmentRecord;
  serverName: string;
}) {
  const settings = environment.settings;
  const domains = settings?.domains ?? [];
  return (
    <Card className={clickableCard}>
      <CardHeader>
        <CardTitle className="truncate">
          <Link
            className={stretchedLink}
            to="/environments/$environmentId"
            params={{ environmentId: environment.id }}
            search={{ services: "", releases: "" }}
          >
            {settings?.name ?? "Environment"}
          </Link>
        </CardTitle>
        <CardAction>
          <EnvironmentStatus environment={environment} />
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-2 text-muted-foreground">
        <span className="flex items-center gap-1.5 truncate">
          <GitBranchIcon className="size-4 shrink-0" />
          {settings?.branch} · {serverName}
          {settings?.autoDeploy && " · Auto-deploy"}
        </span>
        <span className="flex items-center gap-1.5 truncate">
          <GlobeIcon className="size-4 shrink-0" />
          {domains[0]
            ? `${domains[0].hostname}${domains.length > 1 ? ` +${domains.length - 1}` : ""}`
            : "No domain"}
        </span>
      </CardContent>
    </Card>
  );
}

/** First-run guidance; disappears once every prerequisite is in place. */
function SetupChecklist({ hasProject }: { hasProject: boolean }) {
  const panelHost = useSuspenseQuery(serversQuery()).data.servers.find(
    (server) => server.panelHost,
  );
  return panelHost ? (
    <PanelHostChecklist serverId={panelHost.id} hasProject={hasProject} />
  ) : (
    <ChecklistView hasProject={hasProject} tunnelReady={false} />
  );
}

function PanelHostChecklist({
  serverId,
  hasProject,
}: {
  serverId: string;
  hasProject: boolean;
}) {
  const tunnel = useSuspenseQuery(serverTunnelQuery(serverId)).data.tunnel;
  return (
    <ChecklistView
      hasProject={hasProject}
      tunnelReady={tunnel?.phase === ServerTunnelPhase.READY}
    />
  );
}

function ChecklistView({
  hasProject,
  tunnelReady,
}: {
  hasProject: boolean;
  tunnelReady: boolean;
}) {
  const github = useSuspenseQuery(integrationQuery()).data;
  const cloudflare = useSuspenseQuery(cloudflareIntegrationQuery()).data
    .integration;
  const panelHost = useSuspenseQuery(serversQuery()).data.servers.find(
    (server) => server.panelHost,
  );
  const linkClass =
    "w-fit text-sm font-medium underline-offset-4 hover:underline";
  const steps = [
    {
      done: github.installed,
      title: "Connect GitHub",
      detail: github.registered
        ? "Install the App on your repositories."
        : "Create the GitHub App for this panel.",
      link: (
        <Link className={linkClass} to="/settings" hash="github">
          Open settings
        </Link>
      ),
    },
    {
      done: !!cloudflare?.configured,
      title: "Connect Cloudflare",
      detail: "Add an account API token and pick your domain.",
      link: (
        <Link className={linkClass} to="/settings" hash="cloudflare">
          Open settings
        </Link>
      ),
    },
    {
      done: tunnelReady,
      title: "Create the tunnel",
      detail: "Lets public domains reach this server.",
      link: panelHost ? (
        <Link
          className={linkClass}
          to="/servers/$serverId"
          params={{ serverId: panelHost.id }}
        >
          Open server
        </Link>
      ) : (
        <Link className={linkClass} to="/servers" search={{ cursor: "" }}>
          Open servers
        </Link>
      ),
    },
    {
      done: hasProject,
      title: "Add a project",
      detail: "Pick a repository whose CI pushes images.",
      link: (
        <Link
          className={linkClass}
          to="/projects/new"
          search={{ repository: "" }}
        >
          New project
        </Link>
      ),
    },
  ];
  const done = steps.filter((step) => step.done).length;
  if (done === steps.length) return null;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Finish setting up</CardTitle>
        <CardDescription>A few steps before your first deploy.</CardDescription>
        <CardAction>
          <Badge variant="secondary">
            {done} of {steps.length} done
          </Badge>
        </CardAction>
      </CardHeader>
      <CardContent>
        <ol className="flex flex-col gap-4">
          {steps.map((step) => (
            <li key={step.title} className="flex items-start gap-3">
              {step.done ? (
                <CircleCheckIcon
                  className="size-5 shrink-0 text-emerald-500"
                  aria-label="Done"
                />
              ) : (
                <CircleIcon
                  className="size-5 shrink-0 text-muted-foreground"
                  aria-label="To do"
                />
              )}
              <div className="flex flex-col gap-1">
                <span className="font-medium">{step.title}</span>
                {!step.done && (
                  <>
                    <span className="text-muted-foreground">{step.detail}</span>
                    {step.link}
                  </>
                )}
              </div>
            </li>
          ))}
        </ol>
      </CardContent>
    </Card>
  );
}

const projectsCrumb: Crumb = {
  label: "Projects",
  // Crumb links take untyped search; the route defaults cursor to "".
  link: { to: "/projects" },
};

export function Project({ id, cursor }: { id: string; cursor: string }) {
  const project = useSuspenseQuery(projectQuery(id)).data.project;
  const environments = useSuspenseQuery(environmentsQuery(id, cursor));
  const servers = useSuspenseQuery(serversQuery()).data.servers;
  if (!project)
    return (
      <Page title="Project not found" crumbs={[projectsCrumb]}>
        <Empty className="border">
          <EmptyHeader>
            <EmptyTitle>Project not found</EmptyTitle>
            <EmptyDescription>It may have been removed.</EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <ButtonLink to="/projects" search={{ cursor: "" }}>
              Back to projects
            </ButtonLink>
          </EmptyContent>
        </Empty>
      </Page>
    );
  const serverName = (serverId: string) =>
    servers.find((server) => server.id === serverId)?.name ?? "Server";
  const repository = project.repository;

  return (
    <Page
      title={project.name}
      crumbs={[projectsCrumb]}
      description={
        repository && (
          <a
            href={`https://github.com/${repository.owner}/${repository.name}`}
            target="_blank"
            rel="noreferrer"
            className="flex w-fit items-center gap-1.5 hover:text-foreground"
          >
            <FolderGit2Icon />
            {repository.owner}/{repository.name}
          </a>
        )
      }
      actions={
        <ButtonLink
          to="/projects/$projectId/environments/new"
          params={{ projectId: id }}
          search={{ preset: "staging", servers: "" }}
        >
          <PlusIcon data-icon="inline-start" />
          New environment
        </ButtonLink>
      }
    >
      {environments.error && (
        <StaleAlert>
          Couldn’t refresh environments. Showing the last loaded list.
        </StaleAlert>
      )}
      {environments.data.environments.length ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {environments.data.environments.map((environment) => (
            <EnvironmentCard
              key={environment.id}
              environment={environment}
              serverName={serverName(environment.settings?.serverId ?? "")}
            />
          ))}
        </div>
      ) : (
        <Empty className="border">
          <EmptyHeader>
            <EmptyTitle>No environments yet</EmptyTitle>
            <EmptyDescription>
              An environment deploys one branch to a server, with its own
              secrets and domains.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <ButtonLink
              to="/projects/$projectId/environments/new"
              params={{ projectId: id }}
              search={{ preset: "production", servers: "" }}
            >
              Add an environment
            </ButtonLink>
          </EmptyContent>
        </Empty>
      )}
      <CursorPagination
        previous={
          cursor
            ? {
                to: "/projects/$projectId",
                params: { projectId: id },
                search: { cursor: "" },
              }
            : undefined
        }
        next={
          environments.data.page?.nextPageToken
            ? {
                to: "/projects/$projectId",
                params: { projectId: id },
                search: { cursor: environments.data.page.nextPageToken },
              }
            : undefined
        }
      />
    </Page>
  );
}
