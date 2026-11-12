// In-browser fake backend for `pnpm dev:mock`. Production builds never include it.
//
// Scenarios (`?scenario=` once; it sticks for the tab until changed):
//   deployed  everything connected, one live project, a failed and a waiting deploy (default)
//   fresh     nothing connected yet: setup checklist, empty lists
//   errors    like deployed, but every change is rejected
// `?delay=<ms>` sets the response delay (default 300) to inspect loading states.
// Reload the page to reset all data.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate, timestampNow } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";

import {
  CloudflareIntegrationSchema,
  CloudflareService,
  ServerTunnelPhase,
  ServerTunnelSchema,
} from "../../gen/ts/panel/v1/cloudflare_pb";
import {
  AuthService,
  type Environment,
  EnvironmentSchema,
  GitHubService,
  type Operation,
  OperationKind,
  OperationOutcome,
  OperationSchema,
  OperationState,
  PanelService,
  type Project,
  ProjectSchema,
  type Release,
  ReleaseSchema,
  ServerReadiness,
  ServerSchema,
  ServiceHealth,
  ServiceSchema,
  ServiceState,
} from "../../gen/ts/panel/v1/panel_pb";

function setting(name: string, fallback: string) {
  const value = new URLSearchParams(location.search).get(name);
  if (value !== null) sessionStorage.setItem(`mock:${name}`, value);
  return sessionStorage.getItem(`mock:${name}`) ?? fallback;
}

const scenario = setting("scenario", "deployed");
const delay = Number(setting("delay", "300")) || 0;
const fresh = scenario === "fresh";

const ago = (minutes: number) =>
  timestampFromDate(new Date(Date.now() - minutes * 60_000));
const sha = (seed: string) => seed.repeat(40).slice(0, 40);
const digest = (seed: string) => `sha256:${seed.repeat(64).slice(0, 64)}`;

const SERVER = "a7e24511-362b-467a-bd4b-4351f386d097";
const PROJECT = "0d1c3f38-ab51-408b-94b6-7181af76c55a";
const ENV = "3d4a4ae5-1cf5-4a14-a687-cb66af71f6ee";
const ACCOUNT = "ff9ed96ea696cbea199b8c500c61b5fb";
const ZONE = { id: "663c965400b2d968180e51d5843c2e7d", name: "example.com" };

const repositories = [
  { githubRepositoryId: 1048576001n, owner: "acme", name: "telegram-bot" },
  { githubRepositoryId: 1048576002n, owner: "acme", name: "landing" },
  { githubRepositoryId: 1048576003n, owner: "acme", name: "api" },
];

const servers = [
  create(ServerSchema, {
    id: SERVER,
    name: "nixos",
    tailscaleAddress: "100.64.0.10",
    sshHostKeyFingerprint: "SHA256:NwJw+n3LJ7a9EwVto1FJfiU8nxDfZYONcL/iKx1R3+I",
    panelHost: true,
    readiness: ServerReadiness.READY,
    observedAt: ago(1),
  }),
  create(ServerSchema, {
    id: "e1d2c3b4-a596-4877-8695-a4b3c2d1e0f9",
    name: "edge-2",
    tailscaleAddress: "100.64.0.11",
    sshHostKeyFingerprint: "SHA256:Qx7e2v0yH1kq9wJz4Lr8sT3uV6pB5nM2cA1dF0gE7hI",
    readiness: ServerReadiness.UNREACHABLE,
    observedAt: ago(42),
  }),
];

function release(id: string, seed: string, minutes: number) {
  return create(ReleaseSchema, {
    id,
    environmentId: ENV,
    build: {
      githubRepositoryId: repositories[0]!.githubRepositoryId,
      branch: "main",
      sourceSha: sha(seed),
    },
    environmentVersion: 4n,
    images: [{ service: "bot", imageDigest: digest(seed) }],
    domains: [{ hostname: "bot.example.com", service: "bot", port: 3000 }],
    secretVersionId: "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d",
    configurationSha256: seed.repeat(64).slice(0, 64),
    createdAt: ago(minutes),
  });
}

function operation(fields: MessageInitShape<typeof OperationSchema>) {
  return create(OperationSchema, {
    kind: OperationKind.DEPLOY,
    serverId: SERVER,
    environmentId: ENV,
    createdAt: timestampNow(),
    updatedAt: timestampNow(),
    ...fields,
  });
}

const state = {
  authenticated: true,
  github: { registered: true, installed: !fresh, selected: [1048576001n] },
  cloudflare: fresh
    ? create(CloudflareIntegrationSchema, {})
    : create(CloudflareIntegrationSchema, {
        configured: true,
        version: 1n,
        accountId: ACCOUNT,
        tokenId: "7c3e1a9b5d2f4e6a8c0b1d3f5e7a9c2b",
        verifiedAt: ago(30),
        zones: [ZONE],
      }),
  tunnel: create(
    ServerTunnelSchema,
    fresh
      ? { serverId: SERVER }
      : {
          serverId: SERVER,
          accountId: ACCOUNT,
          tunnelId: "a1464e8e-ff9d-4d38-8fb3-0e34d0ab2644",
          phase: ServerTunnelPhase.READY,
          observedAt: ago(3),
          expectedConnectionVersion: 1n,
        },
  ),
  projects: fresh
    ? ([] as Project[])
    : [
        create(ProjectSchema, {
          id: PROJECT,
          name: "telegram-bot",
          repository: repositories[0],
          createdAt: ago(300),
        }),
      ],
  environments: fresh
    ? ([] as Environment[])
    : [
        create(EnvironmentSchema, {
          id: ENV,
          projectId: PROJECT,
          settings: {
            name: "production",
            serverId: SERVER,
            branch: "main",
            composeOverridePath: "compose.production.yaml",
            domains: [
              { hostname: "bot.example.com", service: "bot", port: 3000 },
            ],
            autoDeploy: true,
          },
          version: 4n,
          latestSecretVersionId: "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d",
          secretNames: ["BOT_TOKEN", "WEBHOOK_SECRET", "WEBHOOK_BASE_URL"],
          currentReleaseId: "ff66f37e-ea8a-435c-839d-4121b1cc30a5",
          previousReleaseId: "1f547365-97a6-4403-bb2a-1026e46f0273",
          bindingVersion: 1n,
        }),
      ],
  releases: fresh
    ? ([] as Release[])
    : [
        release("ff66f37e-ea8a-435c-839d-4121b1cc30a5", "b6", 58),
        release("1f547365-97a6-4403-bb2a-1026e46f0273", "e8", 95),
      ],
  operations: fresh
    ? ([] as Operation[])
    : [
        operation({
          id: "c0ffee00-1111-4222-8333-444455556666",
          state: OperationState.WAITING_FOR_BUILD,
          outcome: OperationOutcome.PENDING,
          message: `Waiting for image ghcr.io/acme/telegram-bot:${sha("38")}`,
          createdAt: ago(2),
          updatedAt: ago(1),
        }),
        operation({
          id: "3740e2c6-f674-4a76-ac7f-346f444ab970",
          state: OperationState.FAILED,
          outcome: OperationOutcome.CONFIRMED_FAILURE,
          message: "Service bot exited before becoming healthy.",
          createdAt: ago(80),
          updatedAt: ago(79),
          diagnostics: [
            {
              service: "bot",
              state: "exited",
              exitCode: 1,
              health: "unhealthy",
              logTail:
                "starting telegram-bot\nregistering webhook\nerror: setWebhook failed: dns error: failed to lookup address\nexiting with status 1",
            },
          ],
        }),
        operation({
          id: "b15e7505-484f-5b70-99d5-6051542138ec",
          state: OperationState.SUCCEEDED,
          outcome: OperationOutcome.CONFIRMED_SUCCESS,
          releaseId: "ff66f37e-ea8a-435c-839d-4121b1cc30a5",
          message: "Deployed release.",
          createdAt: ago(58),
          updatedAt: ago(57),
        }),
      ],
};

function find<T extends { id: string }>(items: T[], id: string) {
  const item = items.find((candidate) => candidate.id === id);
  if (!item) throw new ConnectError("not found", Code.NotFound);
  return item;
}

function write() {
  if (scenario === "errors")
    throw new ConnectError(
      "rejected in errors scenario",
      Code.PermissionDenied,
    );
}

const page = { nextPageToken: "" };

/** Runs a fake deploy: queued, running, then a new current release after a few seconds. */
function deploy(environmentId: string, kind: OperationKind, releaseId = "") {
  write();
  const environment = find(state.environments, environmentId);
  const op = operation({
    id: crypto.randomUUID(),
    kind,
    environmentId,
    state: OperationState.RUNNING,
    outcome: OperationOutcome.PENDING,
    previousReleaseId: environment.currentReleaseId,
    message: "Starting services.",
  });
  state.operations.unshift(op);
  setTimeout(() => {
    const next =
      releaseId ||
      (() => {
        const created = release(crypto.randomUUID(), "a1", 0);
        state.releases.unshift(created);
        return created.id;
      })();
    environment.previousReleaseId = environment.currentReleaseId;
    environment.currentReleaseId = next;
    environment.pendingChanges = false;
    Object.assign(op, {
      state: OperationState.SUCCEEDED,
      outcome: OperationOutcome.CONFIRMED_SUCCESS,
      releaseId: next,
      message: "Deployed release.",
      updatedAt: timestampNow(),
    });
  }, 4000);
  return { operation: op };
}

export const mockTransport = createRouterTransport(
  ({ service }) => {
    service(AuthService, {
      getSession: () => ({ authenticated: state.authenticated }),
      login: () => {
        state.authenticated = true;
        return { authenticated: true };
      },
      logout: () => {
        state.authenticated = false;
        return {};
      },
    });

    service(GitHubService, {
      getIntegration: () => ({
        registered: state.github.registered,
        installed: state.github.installed,
        appId: 3141592n,
        ownerId: 99887766n,
        owner: "acme",
      }),
      listRepositories: () => ({
        repositories,
        page,
        selectedRepositoryIds: state.github.selected,
      }),
      selectRepositories: (request) => {
        write();
        state.github.selected = request.repositoryIds;
        return { repositories };
      },
      beginSetup: () => {
        throw new ConnectError(
          "GitHub app setup leaves the panel; use ?scenario=deployed to see the connected state",
          Code.Unimplemented,
        );
      },
      beginInstallation: () => {
        throw new ConnectError(
          "GitHub installation leaves the panel; use ?scenario=deployed to see the connected state",
          Code.Unimplemented,
        );
      },
    });

    service(CloudflareService, {
      getCloudflareIntegration: () => ({ integration: state.cloudflare }),
      listCandidateZones: () => ({
        zones: [
          { ...ZONE, status: "active", type: "full", eligible: true },
          {
            id: "1234567890abcdef1234567890abcdef",
            name: "old-domain.dev",
            status: "pending",
            type: "full",
            eligible: false,
          },
        ],
      }),
      connectAccount: (request) => {
        write();
        state.cloudflare = create(CloudflareIntegrationSchema, {
          configured: true,
          version: state.cloudflare.version + 1n,
          accountId: request.accountId,
          tokenId: "7c3e1a9b5d2f4e6a8c0b1d3f5e7a9c2b",
          verifiedAt: timestampNow(),
          zones: [ZONE].filter((zone) => request.zoneIds.includes(zone.id)),
        });
        return { integration: state.cloudflare };
      },
      getServerTunnel: () => ({ tunnel: state.tunnel }),
      provisionServerTunnel: (request) => {
        write();
        Object.assign(state.tunnel, {
          requestId: request.mutation?.requestId ?? "",
          accountId: state.cloudflare.accountId,
          phase: ServerTunnelPhase.CREATING,
          observedAt: timestampNow(),
        });
        setTimeout(() => {
          state.tunnel.phase = ServerTunnelPhase.READY;
          state.tunnel.tunnelId = "a1464e8e-ff9d-4d38-8fb3-0e34d0ab2644";
          state.tunnel.expectedConnectionVersion = state.cloudflare.version;
        }, 3000);
        return { tunnel: state.tunnel };
      },
    });

    service(PanelService, {
      listServers: () => ({ servers, page }),
      getServer: (request) => ({ server: find(servers, request.id) }),
      listProjects: () => ({ projects: state.projects, page }),
      getProject: (request) => ({
        project: find(state.projects, request.id),
      }),
      createProject: (request) => {
        write();
        const project = create(ProjectSchema, {
          id: crypto.randomUUID(),
          name: request.name,
          repository: request.repository,
          createdAt: timestampNow(),
        });
        state.projects.unshift(project);
        return { project };
      },
      listEnvironments: (request) => ({
        environments: state.environments.filter(
          (environment) => environment.projectId === request.projectId,
        ),
        page,
      }),
      getEnvironment: (request) => ({
        environment: find(state.environments, request.id),
      }),
      createEnvironment: (request) => {
        write();
        const environment = create(EnvironmentSchema, {
          id: crypto.randomUUID(),
          projectId: request.projectId,
          settings: request.settings,
          version: 1n,
          pendingChanges: true,
        });
        state.environments.push(environment);
        return { environment };
      },
      updateEnvironment: (request) => {
        write();
        const environment = find(state.environments, request.id);
        environment.settings = request.settings;
        environment.version += 1n;
        environment.pendingChanges = true;
        return { environment };
      },
      saveEnvironmentSecrets: (request) => {
        write();
        const environment = find(state.environments, request.environmentId);
        const names = new Set(environment.secretNames);
        for (const name of request.removeNames) names.delete(name);
        for (const { name } of request.upserts) names.add(name);
        environment.secretNames = [...names].sort();
        environment.latestSecretVersionId = crypto.randomUUID();
        environment.pendingChanges = true;
        return { environment };
      },
      listServices: (request) => ({
        services:
          request.environmentId === ENV && !fresh
            ? [
                create(ServiceSchema, {
                  name: "bot",
                  environmentId: ENV,
                  state: ServiceState.RUNNING,
                  health: ServiceHealth.HEALTHY,
                  observedImageDigest: digest("b6"),
                  observedReleaseId: find(state.environments, ENV)
                    .currentReleaseId,
                  observedAt: ago(0.5),
                }),
              ]
            : [],
        page,
      }),
      listReleases: (request) => ({
        releases: state.releases.filter(
          (item) => item.environmentId === request.environmentId,
        ),
        page,
      }),
      getRelease: (request) => ({
        release: find(state.releases, request.id),
      }),
      getDeploymentSource: (request) => ({
        sourceSha: sha("38"),
        branch: find(state.environments, request.environmentId).settings
          ?.branch,
        environmentVersion: request.expectedVersion,
      }),
      deployEnvironment: (request) =>
        deploy(request.environmentId, OperationKind.DEPLOY),
      rollbackEnvironment: (request) =>
        deploy(
          request.environmentId,
          OperationKind.ROLLBACK,
          request.releaseId,
        ),
      listOperations: (request) => ({
        operations: state.operations.filter(
          (item) =>
            (!request.environmentId ||
              item.environmentId === request.environmentId) &&
            (!request.serverId || item.serverId === request.serverId),
        ),
        page,
      }),
      getOperation: (request) => ({
        operation: find(state.operations, request.id),
      }),
    });
  },
  {
    router: {
      interceptors: [
        (next) => async (request) => {
          await new Promise((resolve) => setTimeout(resolve, delay));
          return next(request);
        },
      ],
    },
  },
);
