import {
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Outlet,
  redirect,
  stripSearchParams,
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
} from "./api";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty";

import { ButtonLink, ErrorPage } from "./components";
import { Environment } from "./environment";
import { Project, Projects } from "./projects";
import { Server, Servers } from "./servers";
import { Login } from "./session";
import { NavigationProgress, RouteEffects, Shell } from "./shell";

// Defaults are stripped from URLs by each route's stripSearchParams middleware.
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

function Root() {
  return (
    <>
      <RouteEffects />
      <NavigationProgress />
      <Outlet />
    </>
  );
}

const root = createRootRoute({
  component: Root,
  errorComponent: ErrorPage,
  notFoundComponent: () => (
    <Empty className="min-h-svh">
      <EmptyHeader>
        <EmptyTitle>Page not found</EmptyTitle>
        <EmptyDescription>
          Check the address, or start from your projects.
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <ButtonLink to="/projects">Go to projects</ButtonLink>
      </EmptyContent>
    </Empty>
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
    if (session.authenticated) throw redirect({ to: "/projects" });
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
    throw redirect({ to: "/projects" });
  },
});
const projects = createRoute({
  getParentRoute: () => privateRoute,
  path: "/projects",
  validateSearch: pageSearch,
  search: { middlewares: [stripSearchParams({ cursor: "" })] },
  loaderDeps: ({ search }) => search,
  loader: ({ deps }) =>
    required(
      Promise.all([
        queryClient.ensureQueryData(projectsQuery(deps.cursor)),
        queryClient.ensureQueryData(integrationQuery()),
        queryClient.ensureQueryData(cloudflareIntegrationQuery()),
        (async () => {
          const { servers } = await queryClient.ensureQueryData(serversQuery());
          const panelHost = servers.find((server) => server.panelHost);
          if (panelHost)
            await queryClient.ensureQueryData(serverTunnelQuery(panelHost.id));
        })(),
      ]),
    ),
  component: ProjectsRoute,
});
function ProjectsRoute() {
  return <Projects cursor={projects.useSearch().cursor} />;
}

const newProject = createRoute({
  getParentRoute: () => privateRoute,
  path: "/projects/new",
  validateSearch: newProjectSearch,
  search: { middlewares: [stripSearchParams({ repository: "" })] },
  loader: () =>
    required(
      (async () => {
        const integration =
          await queryClient.ensureQueryData(integrationQuery());
        if (!integration.installed) return;
        await queryClient.ensureQueryData(repositoriesQuery());
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
  validateSearch: pageSearch,
  search: { middlewares: [stripSearchParams({ cursor: "" })] },
  loaderDeps: ({ search }) => search,
  loader: ({ params, deps }) =>
    required(
      Promise.all([
        queryClient.ensureQueryData(projectQuery(params.projectId)),
        queryClient.ensureQueryData(
          environmentsQuery(params.projectId, deps.cursor),
        ),
        queryClient.ensureQueryData(serversQuery()),
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
  validateSearch: newEnvironmentSearch,
  search: {
    middlewares: [stripSearchParams({ servers: "", preset: "staging" })],
  },
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
  validateSearch: environmentFormSearch,
  search: { middlewares: [stripSearchParams({ servers: "" })] },
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
  validateSearch: pageSearch,
  search: { middlewares: [stripSearchParams({ cursor: "" })] },
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
  validateSearch: environmentSearch,
  search: { middlewares: [stripSearchParams({ services: "", releases: "" })] },
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
          await Promise.all([
            queryClient.ensureQueryData(
              serverQuery(value.environment.settings.serverId),
            ),
            queryClient.ensureQueryData(
              projectQuery(value.environment.projectId),
            ),
          ]);
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
  validateSearch: operationsSearch,
  search: {
    middlewares: [
      stripSearchParams({ environment: "", server: "", cursor: "" }),
    ],
  },
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
  // Keep the current page while the next one loads; NavigationProgress shows the wait.
  defaultPendingMs: Infinity,
  defaultPreload: false,
  defaultPreloadStaleTime: 0,
  scrollRestoration: true,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
