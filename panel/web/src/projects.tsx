import {
  Badge,
  Banner,
  Empty,
  LayerCard,
  Link,
  Pagination,
  Table,
  Text,
} from "@cloudflare/kumo";
import {
  CheckCircleIcon,
  CircleIcon,
  FolderSimpleIcon,
  GithubLogoIcon,
  PlusIcon,
  WarningIcon,
} from "@phosphor-icons/react";
import { useSuspenseQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";

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
  ButtonLink,
  EnvironmentStatus,
  ObservedTime,
  Page,
} from "./components";

export function Projects({ cursor }: { cursor: string }) {
  const { data, error } = useSuspenseQuery(projectsQuery(cursor));
  const navigate = useNavigate({ from: "/projects" });
  return (
    <Page
      title="Projects"
      description="Apps deployed from your GitHub repositories."
      actions={
        <ButtonLink
          variant="primary"
          icon={<PlusIcon />}
          to="/projects/new"
          search={{ repository: "" }}
        >
          New project
        </ButtonLink>
      }
    >
      <SetupChecklist hasProject={data.projects.length > 0 || !!cursor} />
      {error && (
        <Banner
          variant="alert"
          icon={<WarningIcon weight="fill" />}
          description="Couldn’t refresh projects. Showing the last loaded list."
        />
      )}
      {data.projects.length ? (
        <LayerCard className="p-0">
          <Table>
            <Table.Header>
              <Table.Row>
                <Table.Head>Name</Table.Head>
                <Table.Head>Repository</Table.Head>
                <Table.Head>Created</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {data.projects.map((project) => (
                <Table.Row key={project.id}>
                  <Table.Cell>
                    <Link href={`/projects/${project.id}`}>{project.name}</Link>
                  </Table.Cell>
                  <Table.Cell>
                    {project.repository
                      ? `${project.repository.owner}/${project.repository.name}`
                      : "Repository unavailable"}
                  </Table.Cell>
                  <Table.Cell>
                    <ObservedTime value={project.createdAt} />
                  </Table.Cell>
                </Table.Row>
              ))}
            </Table.Body>
          </Table>
        </LayerCard>
      ) : (
        <Empty
          icon={<FolderSimpleIcon size={48} />}
          title={cursor ? "No more projects" : "No projects yet"}
          description="A project connects one GitHub repository. Its environments decide which branch deploys where."
          contents={
            <ButtonLink
              variant="primary"
              to="/projects/new"
              search={{ repository: "" }}
            >
              New project
            </ButtonLink>
          }
        />
      )}
      {(cursor || data.page?.nextPageToken) && (
        <Pagination
          controls="simple"
          page={cursor ? 2 : 1}
          hasNextPage={!!data.page?.nextPageToken}
          // The API cursor can't step back, so Previous returns to the first page.
          setPage={(page) =>
            void navigate({
              search: {
                cursor:
                  page > (cursor ? 2 : 1)
                    ? (data.page?.nextPageToken ?? "")
                    : "",
              },
            })
          }
        />
      )}
    </Page>
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
      href: "/settings#github",
      action: "Open settings",
    },
    {
      done: !!cloudflare?.configured,
      title: "Connect Cloudflare",
      detail: "Add an account API token and pick your domain.",
      href: "/settings#cloudflare",
      action: "Open settings",
    },
    {
      done: tunnelReady,
      title: "Create the tunnel",
      detail: "Lets public domains reach this server.",
      href: panelHost ? `/servers/${panelHost.id}` : "/servers",
      action: panelHost ? "Open server" : "Open servers",
    },
    {
      done: hasProject,
      title: "Add a project",
      detail: "Pick a repository whose CI pushes images.",
      href: "/projects/new",
      action: "New project",
    },
  ];
  const done = steps.filter((step) => step.done).length;
  if (done === steps.length) return null;
  return (
    <LayerCard>
      <LayerCard.Secondary className="flex items-center justify-between gap-2">
        <span>Finish setting up</span>
        <Badge variant="neutral">
          {done} of {steps.length} done
        </Badge>
      </LayerCard.Secondary>
      <LayerCard.Primary>
        <ol className="flex flex-col gap-4">
          {steps.map((step) => (
            <li key={step.title} className="flex items-start gap-3">
              {step.done ? (
                <CheckCircleIcon
                  size={20}
                  weight="fill"
                  className="shrink-0 text-kumo-success"
                  aria-label="Done"
                />
              ) : (
                <CircleIcon
                  size={20}
                  className="shrink-0 text-kumo-subtle"
                  aria-label="To do"
                />
              )}
              <div className="flex flex-col gap-1">
                <Text bold>{step.title}</Text>
                {!step.done && (
                  <>
                    <Text variant="secondary">{step.detail}</Text>
                    <Link href={step.href}>{step.action}</Link>
                  </>
                )}
              </div>
            </li>
          ))}
        </ol>
      </LayerCard.Primary>
    </LayerCard>
  );
}

export function Project({ id, cursor }: { id: string; cursor: string }) {
  const project = useSuspenseQuery(projectQuery(id)).data.project;
  const environments = useSuspenseQuery(environmentsQuery(id, cursor));
  const servers = useSuspenseQuery(serversQuery()).data.servers;
  const navigate = useNavigate({ from: "/projects/$projectId" });
  if (!project)
    return (
      <Page
        title="Project not found"
        crumbs={[{ label: "Projects", href: "/projects" }]}
      >
        <Empty
          title="Project not found"
          description="It may have been removed."
          contents={
            <ButtonLink to="/projects" search={{ cursor: "" }}>
              Back to projects
            </ButtonLink>
          }
        />
      </Page>
    );
  const serverName = (serverId: string) =>
    servers.find((server) => server.id === serverId)?.name ?? "Server";

  return (
    <Page
      title={project.name}
      crumbs={[{ label: "Projects", href: "/projects" }]}
      actions={
        <ButtonLink
          variant="primary"
          icon={<PlusIcon />}
          to="/projects/$projectId/environments/new"
          params={{ projectId: id }}
          search={{ preset: "staging", servers: "" }}
        >
          New environment
        </ButtonLink>
      }
    >
      {project.repository && (
        <Link
          href={`https://github.com/${project.repository.owner}/${project.repository.name}`}
          target="_blank"
          rel="noreferrer"
          className="flex w-fit items-center gap-1.5 pl-3"
        >
          <GithubLogoIcon aria-hidden="true" />
          {project.repository.owner}/{project.repository.name}
        </Link>
      )}
      {environments.error && (
        <Banner
          variant="alert"
          icon={<WarningIcon weight="fill" />}
          description="Couldn’t refresh environments. Showing the last loaded list."
        />
      )}
      {environments.data.environments.length ? (
        <LayerCard className="p-0">
          <Table>
            <Table.Header>
              <Table.Row>
                <Table.Head>Environment</Table.Head>
                <Table.Head>Status</Table.Head>
                <Table.Head>Branch</Table.Head>
                <Table.Head>Server</Table.Head>
                <Table.Head>Domain</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {environments.data.environments.map((environment) => {
                const settings = environment.settings;
                return (
                  <Table.Row key={environment.id}>
                    <Table.Cell>
                      <Link href={`/environments/${environment.id}`}>
                        {settings?.name ?? "Environment"}
                      </Link>
                    </Table.Cell>
                    <Table.Cell>
                      <div className="flex flex-wrap gap-1">
                        <EnvironmentStatus environment={environment} />
                        {settings?.autoDeploy && (
                          <Badge variant="outline">Auto-deploy</Badge>
                        )}
                      </div>
                    </Table.Cell>
                    <Table.Cell>{settings?.branch}</Table.Cell>
                    <Table.Cell>
                      {serverName(settings?.serverId ?? "")}
                    </Table.Cell>
                    <Table.Cell>
                      {settings?.domains[0]
                        ? `${settings.domains[0].hostname}${
                            settings.domains.length > 1
                              ? ` +${settings.domains.length - 1}`
                              : ""
                          }`
                        : "—"}
                    </Table.Cell>
                  </Table.Row>
                );
              })}
            </Table.Body>
          </Table>
        </LayerCard>
      ) : (
        <Empty
          title="No environments yet"
          description="An environment deploys one branch to a server, with its own secrets and domains."
          contents={
            <ButtonLink
              variant="primary"
              to="/projects/$projectId/environments/new"
              params={{ projectId: id }}
              search={{ preset: "production", servers: "" }}
            >
              Add an environment
            </ButtonLink>
          }
        />
      )}
      {(cursor || environments.data.page?.nextPageToken) && (
        <Pagination
          controls="simple"
          page={cursor ? 2 : 1}
          hasNextPage={!!environments.data.page?.nextPageToken}
          // The API cursor can't step back, so Previous returns to the first page.
          setPage={(page) =>
            void navigate({
              search: {
                cursor:
                  page > (cursor ? 2 : 1)
                    ? (environments.data.page?.nextPageToken ?? "")
                    : "",
              },
            })
          }
        />
      )}
    </Page>
  );
}
