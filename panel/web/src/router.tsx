import {
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Link,
  Outlet,
  redirect,
} from "@tanstack/react-router";
import { z } from "zod";

import {
  cloudflareIntegrationQuery,
  environmentQuery,
  environmentsQuery,
  integrationQuery,
  operationQuery,
  operationsQuery,
  projectQuery,
  projectsQuery,
  queryClient,
  releaseQuery,
  releasesQuery,
  repositoriesQuery,
  serverQuery,
  serverTunnelQuery,
  serversQuery,
  servicesQuery,
  sessionQuery,
  unauthenticated,
  workflowsQuery,
} from "./api";
import { ErrorPage, InitialLoading, PageTitle } from "./components";
import { Environment } from "./environment";
import { Project, Projects } from "./projects";
import { Server, Servers } from "./servers";
import { Login } from "./session";
import { NavigationProgress, Shell } from "./shell";

const cursor = z.string().max(512).catch("");
const pageSearch = z.object({ cursor: cursor.default("") });
const environmentSearch = z.object({
  services: cursor.default(""),
  releases: cursor.default(""),
});
const environmentFormSearch = z.object({ servers: cursor.default("") });
const newEnvironmentSearch = environmentFormSearch.extend({
  preset: z.enum(["staging", "production"]).catch("staging").default("staging"),
});
const id = z.uuid();
const operationsSearch = z.object({
  environment: z.union([id, z.literal("")]).default(""),
  server: z.union([id, z.literal("")]).default(""),
  cursor: cursor.default(""),
});
const repositoryId = z
  .string()
  .regex(/^[1-9][0-9]{0,18}$/)
  .refine((value) => BigInt(value) <= 9_223_372_036_854_775_807n)
  .catch("");
const newProjectSearch = z.object({
  repository: repositoryId.default(""),
  workflows: cursor.default(""),
});

async function required<T>(value: Promise<T>): Promise<T> {
  try {
    return await value;
  } catch (error) {
    if (unauthenticated(error)) {
      await queryClient.cancelQueries();
      queryClient.clear();
      throw redirect({ to: "/login" });
    }
    throw error;
  }
}

const root = createRootRoute({
  component: () => (
    <>
      <NavigationProgress />
      <Outlet />
    </>
  ),
  errorComponent: ErrorPage,
  notFoundComponent: () => (
    <main className="not-found">
      <PageTitle
        title="Page not found"
        description="Check the address or return to your projects."
      />
      <Link to="/projects" search={{ cursor: "" }}>
        Projects
      </Link>
    </main>
  ),
});

const login = createRoute({
  getParentRoute: () => root,
  path: "/login",
  beforeLoad: async () => {
    const session = await queryClient.fetchQuery({
      ...sessionQuery(),
      staleTime: 0,
    });
    if (session.authenticated)
      throw redirect({ to: "/projects", search: { cursor: "" } });
  },
  component: Login,
});

const privateRoute = createRoute({
  getParentRoute: () => root,
  id: "_private",
  beforeLoad: async () => {
    const session = await queryClient.fetchQuery({
      ...sessionQuery(),
      staleTime: 0,
    });
    if (!session.authenticated) {
      await queryClient.cancelQueries();
      queryClient.clear();
      throw redirect({ to: "/login" });
    }
  },
  component: Shell,
});

const index = createRoute({
  getParentRoute: () => privateRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/projects", search: { cursor: "" } });
  },
});
const projects = createRoute({
  getParentRoute: () => privateRoute,
  path: "/projects",
  validateSearch: (search) => pageSearch.parse(search),
  loaderDeps: ({ search }) => search,
  loader: ({ deps }) =>
    required(queryClient.ensureQueryData(projectsQuery(deps.cursor))),
  component: ProjectsRoute,
});
function ProjectsRoute() {
  return <Projects cursor={projects.useSearch().cursor} />;
}

const newProject = createRoute({
  getParentRoute: () => privateRoute,
  path: "/projects/new",
  validateSearch: (search) => newProjectSearch.parse(search),
  loaderDeps: ({ search }) => search,
  loader: ({ deps }) =>
    required(
      (async () => {
        const integration =
          await queryClient.ensureQueryData(integrationQuery());
        if (!integration.installed) return;
        const repositories =
          await queryClient.ensureQueryData(repositoriesQuery());
        if (
          deps.repository &&
          repositories.selectedRepositoryIds.includes(BigInt(deps.repository))
        )
          await queryClient.ensureQueryData(
            workflowsQuery(BigInt(deps.repository), deps.workflows),
          );
      })(),
    ),
  component: lazyRouteComponent(
    () => import("./create-project"),
    "NewProjectRoute",
  ),
});

const project = createRoute({
  getParentRoute: () => privateRoute,
  path: "/projects/$projectId",
  params: { parse: (params) => ({ projectId: id.parse(params.projectId) }) },
  validateSearch: (search) => pageSearch.parse(search),
  loaderDeps: ({ search }) => search,
  loader: ({ params, deps }) =>
    required(
      Promise.all([
        queryClient.ensureQueryData(projectQuery(params.projectId)),
        queryClient.ensureQueryData(
          environmentsQuery(params.projectId, deps.cursor),
        ),
      ]),
    ),
  component: ProjectRoute,
});
function ProjectRoute() {
  return (
    <Project
      id={project.useParams().projectId}
      cursor={project.useSearch().cursor}
    />
  );
}

const newEnvironment = createRoute({
  getParentRoute: () => privateRoute,
  path: "/projects/$projectId/environments/new",
  params: { parse: (params) => ({ projectId: id.parse(params.projectId) }) },
  validateSearch: (search) => newEnvironmentSearch.parse(search),
  loaderDeps: ({ search }) => search,
  loader: ({ params, deps }) =>
    required(
      Promise.all([
        queryClient.ensureQueryData(projectQuery(params.projectId)),
        queryClient.ensureQueryData(serversQuery(deps.servers)),
      ]),
    ),
  component: lazyRouteComponent(
    () => import("./environment-settings"),
    "NewEnvironmentRoute",
  ),
});

const environmentSettings = createRoute({
  getParentRoute: () => privateRoute,
  path: "/environments/$environmentId/settings",
  params: {
    parse: (params) => ({ environmentId: id.parse(params.environmentId) }),
  },
  validateSearch: (search) => environmentFormSearch.parse(search),
  loaderDeps: ({ search }) => search,
  loader: ({ params, deps }) =>
    required(
      (async () => {
        const [record] = await Promise.all([
          queryClient.ensureQueryData(environmentQuery(params.environmentId)),
          queryClient.ensureQueryData(serversQuery(deps.servers)),
        ]);
        return record.environment?.settings
          ? (
              await queryClient.ensureQueryData(
                serverQuery(record.environment.settings.serverId),
              )
            ).server
          : undefined;
      })(),
    ),
  component: lazyRouteComponent(
    () => import("./environment-settings"),
    "EnvironmentSettingsRoute",
  ),
});

const environmentSecrets = createRoute({
  getParentRoute: () => privateRoute,
  path: "/environments/$environmentId/secrets",
  params: {
    parse: (params) => ({ environmentId: id.parse(params.environmentId) }),
  },
  loader: ({ params }) =>
    required(
      queryClient.ensureQueryData(environmentQuery(params.environmentId)),
    ),
  component: lazyRouteComponent(
    () => import("./environment-secrets"),
    "EnvironmentSecretsRoute",
  ),
});

const servers = createRoute({
  getParentRoute: () => privateRoute,
  path: "/servers",
  validateSearch: (search) => pageSearch.parse(search),
  loaderDeps: ({ search }) => search,
  loader: ({ deps }) =>
    required(queryClient.ensureQueryData(serversQuery(deps.cursor))),
  component: ServersRoute,
});
function ServersRoute() {
  return <Servers cursor={servers.useSearch().cursor} />;
}

const server = createRoute({
  getParentRoute: () => privateRoute,
  path: "/servers/$serverId",
  params: { parse: (params) => ({ serverId: id.parse(params.serverId) }) },
  loader: ({ params }) =>
    required(
      Promise.all([
        queryClient.ensureQueryData(serverQuery(params.serverId)),
        queryClient.ensureQueryData(serverTunnelQuery(params.serverId)),
        queryClient.ensureQueryData(cloudflareIntegrationQuery()),
      ]),
    ),
  component: ServerRoute,
});
function ServerRoute() {
  return <Server id={server.useParams().serverId} />;
}

const environment = createRoute({
  getParentRoute: () => privateRoute,
  path: "/environments/$environmentId",
  params: {
    parse: (params) => ({ environmentId: id.parse(params.environmentId) }),
  },
  validateSearch: (search) => environmentSearch.parse(search),
  loaderDeps: ({ search }) => search,
  loader: async ({ params, deps }) =>
    required(
      (async () => {
        const [value] = await Promise.all([
          queryClient.ensureQueryData(environmentQuery(params.environmentId)),
          queryClient.ensureQueryData(
            servicesQuery(params.environmentId, deps.services),
          ),
          queryClient.ensureQueryData(
            releasesQuery(params.environmentId, deps.releases),
          ),
        ]);
        if (value.environment?.settings)
          await queryClient.ensureQueryData(
            serverQuery(value.environment.settings.serverId),
          );
      })(),
    ),
  component: EnvironmentRoute,
});
function EnvironmentRoute() {
  const search = environment.useSearch();
  return (
    <Environment
      id={environment.useParams().environmentId}
      services={search.services}
      releases={search.releases}
    />
  );
}

const operations = createRoute({
  getParentRoute: () => privateRoute,
  path: "/operations",
  validateSearch: (search) => operationsSearch.parse(search),
  loaderDeps: ({ search }) => search,
  loader: ({ deps }) =>
    required(
      queryClient.ensureQueryData(
        operationsQuery(deps.environment, deps.server, deps.cursor),
      ),
    ),
  component: lazyRouteComponent(
    () => import("./operation-routes"),
    "OperationsRoute",
  ),
});

const operation = createRoute({
  getParentRoute: () => privateRoute,
  path: "/operations/$operationId",
  params: {
    parse: (params) => ({ operationId: id.parse(params.operationId) }),
  },
  loader: ({ params }) =>
    required(queryClient.ensureQueryData(operationQuery(params.operationId))),
  component: lazyRouteComponent(
    () => import("./operation-routes"),
    "OperationRoute",
  ),
});

const release = createRoute({
  getParentRoute: () => privateRoute,
  path: "/releases/$releaseId",
  params: { parse: (params) => ({ releaseId: id.parse(params.releaseId) }) },
  loader: ({ params }) =>
    required(
      (async () => {
        const result = await queryClient.ensureQueryData(
          releaseQuery(params.releaseId),
        );
        if (result.release)
          await queryClient.ensureQueryData(
            environmentQuery(result.release.environmentId),
          );
      })(),
    ),
  component: lazyRouteComponent(
    () => import("./operation-routes"),
    "ReleaseRoute",
  ),
});

const settings = createRoute({
  getParentRoute: () => privateRoute,
  path: "/settings",
  loader: () =>
    required(
      Promise.all([
        queryClient.ensureQueryData(integrationQuery()),
        queryClient.ensureQueryData(cloudflareIntegrationQuery()),
      ]),
    ),
  component: lazyRouteComponent(() => import("./settings"), "Settings"),
});

const repositorySelection = createRoute({
  getParentRoute: () => privateRoute,
  path: "/settings/repositories",
  loader: () =>
    required(
      (async () => {
        const integration =
          await queryClient.ensureQueryData(integrationQuery());
        if (integration.installed)
          await queryClient.ensureQueryData(repositoriesQuery());
      })(),
    ),
  component: lazyRouteComponent(
    () => import("./repository-selection"),
    "RepositorySelection",
  ),
});

export const router = createRouter({
  routeTree: root.addChildren([
    login,
    privateRoute.addChildren([
      index,
      projects,
      newProject,
      project,
      newEnvironment,
      servers,
      server,
      environment,
      environmentSettings,
      environmentSecrets,
      operations,
      operation,
      release,
      settings,
      repositorySelection,
    ]),
  ]),
  defaultPendingMs: Infinity,
  defaultPendingComponent: InitialLoading,
  defaultPreload: false,
  defaultPreloadStaleTime: 0,
  scrollRestoration: true,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
