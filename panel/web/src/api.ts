import { Code, ConnectError, createClient } from "@connectrpc/connect";
import {
  createConnectQueryKey,
  createQueryOptions,
} from "@connectrpc/connect-query";
import { createConnectTransport } from "@connectrpc/connect-web";
import { QueryClient } from "@tanstack/react-query";

import {
  AuthService,
  type Environment,
  GitHubService,
  PanelService,
} from "../../gen/ts/panel/v1/panel_pb";
import { CloudflareService } from "../../gen/ts/panel/v1/cloudflare_pb";

export const transport = createConnectTransport({
  baseUrl: window.location.origin,
  defaultTimeoutMs: 12_000,
  fetch: (input, init) =>
    fetch(input, { ...init, credentials: "same-origin", cache: "no-store" }),
});

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      gcTime: 300_000,
      retry: false,
      refetchOnWindowFocus: true,
    },
    mutations: { retry: false },
  },
});

export const auth = createClient(AuthService, transport);
export const github = createClient(GitHubService, transport);
export const cloudflare = createClient(CloudflareService, transport);
export const panel = createClient(PanelService, transport);
export const sessionQuery = () =>
  createQueryOptions(AuthService.method.getSession, {}, { transport });
export const projectsQuery = (cursor = "") =>
  createQueryOptions(
    PanelService.method.listProjects,
    { page: { pageSize: 50, pageToken: cursor } },
    { transport },
  );
export const projectQuery = (id: string) =>
  createQueryOptions(PanelService.method.getProject, { id }, { transport });
export const environmentsQuery = (projectId: string, cursor = "") =>
  createQueryOptions(
    PanelService.method.listEnvironments,
    { projectId, page: { pageSize: 50, pageToken: cursor } },
    { transport },
  );
export const environmentListKey = (projectId: string) =>
  createConnectQueryKey({
    schema: PanelService.method.listEnvironments,
    input: { projectId },
    cardinality: "finite",
    transport,
  });
export const environmentQuery = (id: string) =>
  createQueryOptions(PanelService.method.getEnvironment, { id }, { transport });

export async function refreshEnvironment(id: string) {
  await queryClient.cancelQueries({ queryKey: environmentQuery(id).queryKey });
  const result = await queryClient.fetchQuery({
    ...environmentQuery(id),
    staleTime: 0,
  });
  if (!result.environment?.settings)
    throw new Error("Environment metadata unavailable");
  return result.environment;
}
export const serversQuery = (cursor = "") =>
  createQueryOptions(
    PanelService.method.listServers,
    { page: { pageSize: 50, pageToken: cursor } },
    { transport },
  );
export const serverQuery = (id: string) =>
  createQueryOptions(PanelService.method.getServer, { id }, { transport });
export const serverTunnelQuery = (serverId: string) =>
  createQueryOptions(
    CloudflareService.method.getServerTunnel,
    { serverId },
    { transport },
  );
export const servicesQuery = (environmentId: string, cursor = "") =>
  createQueryOptions(
    PanelService.method.listServices,
    { environmentId, page: { pageSize: 64, pageToken: cursor } },
    { transport },
  );
export const releasesQuery = (environmentId: string, cursor = "") =>
  createQueryOptions(
    PanelService.method.listReleases,
    { environmentId, page: { pageSize: 20, pageToken: cursor } },
    { transport },
  );
export const releaseQuery = (id: string) =>
  createQueryOptions(PanelService.method.getRelease, { id }, { transport });
export const operationsQuery = (
  environmentId = "",
  serverId = "",
  cursor = "",
) =>
  createQueryOptions(
    PanelService.method.listOperations,
    {
      ...(environmentId ? { environmentId } : {}),
      ...(serverId ? { serverId } : {}),
      page: { pageSize: 50, pageToken: cursor },
    },
    { transport },
  );
export const operationQuery = (id: string) =>
  createQueryOptions(PanelService.method.getOperation, { id }, { transport });

export async function invalidateDeploymentMetadata(environment: Environment) {
  const keys = [
    environmentQuery(environment.id).queryKey,
    environmentListKey(environment.projectId),
    createConnectQueryKey({
      schema: PanelService.method.listServices,
      input: { environmentId: environment.id },
      cardinality: "finite",
      transport,
    }),
    createConnectQueryKey({
      schema: PanelService.method.listReleases,
      input: { environmentId: environment.id },
      cardinality: "finite",
      transport,
    }),
    createConnectQueryKey({
      schema: PanelService.method.listOperations,
      cardinality: "finite",
      transport,
    }),
  ];
  await Promise.all(
    keys.map((queryKey) => queryClient.invalidateQueries({ queryKey })),
  );
}
export const integrationQuery = () =>
  createQueryOptions(GitHubService.method.getIntegration, {}, { transport });
export const cloudflareIntegrationQuery = () =>
  createQueryOptions(
    CloudflareService.method.getCloudflareIntegration,
    {},
    { transport },
  );
export const repositoriesQuery = () =>
  createQueryOptions(
    GitHubService.method.listRepositories,
    { page: { pageSize: 100 } },
    { transport },
  );
export const workflowsQuery = (repositoryId: bigint, cursor = "") =>
  createQueryOptions(
    GitHubService.method.listWorkflows,
    { repositoryId, page: { pageSize: 50, pageToken: cursor } },
    { transport },
  );

export function mutationRejected(error: unknown) {
  return (
    error instanceof ConnectError &&
    [
      Code.InvalidArgument,
      Code.PermissionDenied,
      Code.Unauthenticated,
      Code.FailedPrecondition,
      Code.NotFound,
      Code.AlreadyExists,
      Code.Aborted,
      Code.ResourceExhausted,
      Code.Unimplemented,
    ].includes(error.code)
  );
}

export function unauthenticated(error: unknown) {
  return error instanceof ConnectError && error.code === Code.Unauthenticated;
}

export function errorMessage(error: unknown): string {
  if (!(error instanceof ConnectError))
    return "Something went wrong. Please try again.";
  switch (error.code) {
    case Code.Unauthenticated:
      return "Your session has expired. Sign in to continue.";
    case Code.NotFound:
      return "This resource could not be found.";
    case Code.InvalidArgument:
      return "Check the address or page filter and try again.";
    case Code.PermissionDenied:
      return "This request was refused. Check that you are using the configured panel address.";
    case Code.ResourceExhausted:
      return "Too many requests. Wait before trying again.";
    case Code.DeadlineExceeded:
    case Code.Unavailable:
      return "The panel could not be reached. Check your connection and try again.";
    default:
      return "The request could not be completed. Please try again.";
  }
}
