import {
  CaretRightIcon,
  FolderSimpleIcon,
  GitBranchIcon,
  GithubLogoIcon,
  GlobeIcon,
  HardDrivesIcon,
  LightningIcon,
  PlusIcon,
} from "@phosphor-icons/react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";

import { ServerTunnelPhase } from "../../gen/ts/panel/v1/cloudflare_pb";
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
  Banner,
  Card,
  Empty,
  EnvironmentStatus,
  ButtonLink,
  ObservedTime,
  PageHeader,
} from "./components";

export function Projects({ cursor }: { cursor: string }) {
  const { data, error } = useSuspenseQuery(projectsQuery(cursor));
  return (
    <>
      <PageHeader
        title="Projects"
        description="Apps deployed from your GitHub repositories."
      >
        <ButtonLink
          variant="primary"
          to="/projects/new"
          search={{ repository: "" }}
        >
          <PlusIcon size={16} aria-hidden="true" />
          New project
        </ButtonLink>
      </PageHeader>
      <div className="stack">
        <SetupChecklist hasProject={data.projects.length > 0 || !!cursor} />
        {error && (
          <Banner tone="warning">
            Couldn’t refresh projects. Showing the last loaded list.
          </Banner>
        )}
        <section className="card" aria-label="Projects">
          {data.projects.length ? (
            <ul className="list">
              {data.projects.map((project) => (
                <li key={project.id}>
                  <Link
                    className="list-row"
                    to="/projects/$projectId"
                    params={{ projectId: project.id }}
                    search={{ cursor: "" }}
                  >
                    <span className="empty-icon" style={{ margin: 0 }}>
                      <FolderSimpleIcon size={18} aria-hidden="true" />
                    </span>
                    <span className="list-main">
                      <span className="list-title">{project.name}</span>
                      <span className="list-meta">
                        {project.repository ? (
                          <span className="row" style={{ gap: "0.25rem" }}>
                            <GithubLogoIcon size={14} aria-hidden="true" />
                            {project.repository.owner}/{project.repository.name}
                          </span>
                        ) : (
                          "Repository unavailable"
                        )}
                      </span>
                    </span>
                    <span className="list-aside">
                      <ObservedTime
                        value={project.createdAt}
                        prefix="Created"
                      />
                      <CaretRightIcon className="chevron" size={16} />
                    </span>
                  </Link>
                </li>
              ))}
            </ul>
          ) : (
            <Empty
              icon={<FolderSimpleIcon size={20} />}
              title={cursor ? "No more projects" : "No projects yet"}
              actions={
                <ButtonLink
                  variant="primary"
                  to="/projects/new"
                  search={{ repository: "" }}
                >
                  New project
                </ButtonLink>
              }
            >
              A project connects one GitHub repository. Its environments decide
              which branch deploys where.
            </Empty>
          )}
          {(cursor || data.page?.nextPageToken) && (
            <nav className="pagination" aria-label="Project pages">
              {cursor && (
                <ButtonLink
                  variant="secondary"
                  size="sm"
                  to="/projects"
                  search={{ cursor: "" }}
                >
                  Newest
                </ButtonLink>
              )}
              {data.page?.nextPageToken && (
                <ButtonLink
                  variant="secondary"
                  size="sm"
                  to="/projects"
                  search={{ cursor: data.page.nextPageToken }}
                >
                  Older
                </ButtonLink>
              )}
            </nav>
          )}
        </section>
      </div>
    </>
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
  const steps = [
    {
      done: github.installed,
      title: "Connect GitHub",
      detail: github.registered
        ? "Install the App on your repositories."
        : "Create the GitHub App for this panel.",
      link: (
        <Link to="/settings" hash="github">
          Open settings
        </Link>
      ),
    },
    {
      done: !!cloudflare?.configured,
      title: "Connect Cloudflare",
      detail: "Add an account API token and pick your domain.",
      link: (
        <Link to="/settings" hash="cloudflare">
          Open settings
        </Link>
      ),
    },
    {
      done: tunnelReady,
      title: "Create the tunnel",
      detail: "Lets public domains reach this server.",
      link: panelHost ? (
        <Link to="/servers/$serverId" params={{ serverId: panelHost.id }}>
          Open {panelHost.name}
        </Link>
      ) : (
        <Link to="/servers" search={{ cursor: "" }}>
          Open servers
        </Link>
      ),
    },
    {
      done: hasProject,
      title: "Add a project",
      detail: "Pick a repository whose CI pushes images.",
      link: (
        <Link to="/projects/new" search={{ repository: "" }}>
          New project
        </Link>
      ),
    },
  ];
  const done = steps.filter((step) => step.done).length;
  if (done === steps.length) return null;
  return (
    <Card
      title="Finish setting up"
      description="Complete these steps to deploy your first app."
      labelledBy="setup-title"
      actions={
        <span className="row subtle">
          <span className="progress" aria-hidden="true">
            <span style={{ width: `${(done / steps.length) * 100}%` }} />
          </span>
          {done} of {steps.length}
        </span>
      }
      flush
    >
      <ol className="checklist">
        {steps.map((step, index) => (
          <li key={step.title} className={step.done ? "check-done" : undefined}>
            <span className="check-icon" aria-hidden="true">
              {step.done ? "✓" : index + 1}
            </span>
            <span className="check-text">
              <strong>
                {step.title}
                <span className="visually-hidden">
                  {step.done ? " (done)" : " (to do)"}
                </span>
              </strong>
              {!step.done && (
                <>
                  <span className="subtle">{step.detail}</span>
                  <span style={{ fontSize: "0.8125rem" }}>{step.link}</span>
                </>
              )}
            </span>
          </li>
        ))}
      </ol>
    </Card>
  );
}

export function Project({ id, cursor }: { id: string; cursor: string }) {
  const project = useSuspenseQuery(projectQuery(id)).data.project;
  const environments = useSuspenseQuery(environmentsQuery(id, cursor));
  const servers = useSuspenseQuery(serversQuery()).data.servers;
  if (!project)
    return (
      <Card>
        <Empty title="Project not found">
          It may have been removed.{" "}
          <Link to="/projects" search={{ cursor: "" }}>
            Back to projects
          </Link>
        </Empty>
      </Card>
    );
  const serverName = (serverId: string) =>
    servers.find((server) => server.id === serverId)?.name ?? "Server";

  return (
    <>
      <PageHeader
        breadcrumbs={[
          <Link key="projects" to="/projects" search={{ cursor: "" }}>
            Projects
          </Link>,
        ]}
        title={project.name}
        description={
          project.repository && (
            <a
              className="row"
              style={{ gap: "0.375rem", color: "inherit" }}
              href={`https://github.com/${project.repository.owner}/${project.repository.name}`}
              target="_blank"
              rel="noreferrer"
            >
              <GithubLogoIcon size={16} aria-hidden="true" />
              {project.repository.owner}/{project.repository.name}
            </a>
          )
        }
      >
        <ButtonLink
          variant="primary"
          to="/projects/$projectId/environments/new"
          params={{ projectId: id }}
          search={{ preset: "staging", servers: "" }}
        >
          <PlusIcon size={16} aria-hidden="true" />
          New environment
        </ButtonLink>
      </PageHeader>
      <div className="stack">
        {environments.error && (
          <Banner tone="warning">
            Couldn’t refresh environments. Showing the last loaded list.
          </Banner>
        )}
        <Card title="Environments" labelledBy="environments-title" flush>
          {environments.data.environments.length ? (
            <ul className="list">
              {environments.data.environments.map((environment) => {
                const settings = environment.settings;
                return (
                  <li key={environment.id}>
                    <Link
                      className="list-row"
                      to="/environments/$environmentId"
                      params={{ environmentId: environment.id }}
                      search={{ services: "", releases: "" }}
                    >
                      <span className="list-main">
                        <span className="list-title">
                          {settings?.name ?? "Environment"}
                          <EnvironmentStatus environment={environment} />
                        </span>
                        <span className="list-meta">
                          <span className="row" style={{ gap: "0.25rem" }}>
                            <GitBranchIcon size={14} aria-hidden="true" />
                            {settings?.branch}
                          </span>
                          <span className="row" style={{ gap: "0.25rem" }}>
                            <HardDrivesIcon size={14} aria-hidden="true" />
                            {serverName(settings?.serverId ?? "")}
                          </span>
                          {settings?.domains[0] && (
                            <span className="row" style={{ gap: "0.25rem" }}>
                              <GlobeIcon size={14} aria-hidden="true" />
                              {settings.domains[0].hostname}
                              {settings.domains.length > 1 &&
                                ` +${settings.domains.length - 1}`}
                            </span>
                          )}
                          {settings?.autoDeploy && (
                            <span className="row" style={{ gap: "0.25rem" }}>
                              <LightningIcon size={14} aria-hidden="true" />
                              Auto-deploy
                            </span>
                          )}
                        </span>
                      </span>
                      <span className="list-aside">
                        <CaretRightIcon className="chevron" size={16} />
                      </span>
                    </Link>
                  </li>
                );
              })}
            </ul>
          ) : (
            <Empty
              title="No environments yet"
              actions={
                <ButtonLink
                  variant="primary"
                  to="/projects/$projectId/environments/new"
                  params={{ projectId: id }}
                  search={{ preset: "production", servers: "" }}
                >
                  Add an environment
                </ButtonLink>
              }
            >
              An environment deploys one branch to a server, with its own
              secrets and domains.
            </Empty>
          )}
          {(cursor || environments.data.page?.nextPageToken) && (
            <nav className="pagination" aria-label="Environment pages">
              {cursor && (
                <ButtonLink
                  variant="secondary"
                  size="sm"
                  to="/projects/$projectId"
                  params={{ projectId: id }}
                  search={{ cursor: "" }}
                >
                  Newest
                </ButtonLink>
              )}
              {environments.data.page?.nextPageToken && (
                <ButtonLink
                  variant="secondary"
                  size="sm"
                  to="/projects/$projectId"
                  params={{ projectId: id }}
                  search={{ cursor: environments.data.page.nextPageToken }}
                >
                  Older
                </ButtonLink>
              )}
            </nav>
          )}
        </Card>
      </div>
    </>
  );
}
