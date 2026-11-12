import {
  type Operation,
  OperationKind,
  OperationOutcome,
  OperationState,
} from "../../gen/ts/panel/v1/panel_pb";
import { Badge, type Tone } from "./components";

const kinds: Record<OperationKind, string> = {
  [OperationKind.UNSPECIFIED]: "Operation",
  [OperationKind.ENROLL_SERVER]: "Server setup",
  [OperationKind.DEPLOY]: "Deployment",
  [OperationKind.ROLLBACK]: "Rollback",
};
const states: Record<OperationState, [string, Tone]> = {
  [OperationState.UNSPECIFIED]: ["Unknown", "neutral"],
  [OperationState.QUEUED]: ["Queued", "info"],
  [OperationState.RUNNING]: ["Deploying", "info"],
  [OperationState.SUCCEEDED]: ["Succeeded", "success"],
  [OperationState.FAILED]: ["Failed", "danger"],
  [OperationState.RECOVERING]: ["Restoring", "warning"],
  [OperationState.RECOVERED]: ["Rolled back", "warning"],
  [OperationState.SUPERSEDED]: ["Superseded", "neutral"],
  [OperationState.NEEDS_ATTENTION]: ["Needs attention", "warning"],
  [OperationState.WAITING_FOR_BUILD]: ["Waiting for build", "info"],
};
export function operationKind(value: OperationKind) {
  return kinds[value] ?? "Operation";
}

export function OperationStatus({
  value,
  active,
}: {
  value: OperationState;
  active?: boolean;
}) {
  const [label, tone] = states[value] ?? ["Unknown", "neutral"];
  return (
    <Badge tone={tone} active={active}>
      {label}
    </Badge>
  );
}

export function operationActive(value: Operation) {
  const unknownDeployment =
    value.state === OperationState.NEEDS_ATTENTION &&
    value.releaseId !== "" &&
    (value.outcome === OperationOutcome.UNKNOWN ||
      value.outcome === OperationOutcome.PARTIAL);
  return (
    unknownDeployment ||
    [
      OperationState.QUEUED,
      OperationState.RUNNING,
      OperationState.RECOVERING,
      OperationState.WAITING_FOR_BUILD,
    ].includes(value.state)
  );
}

export type Step = {
  title: string;
  detail?: string;
  status: "done" | "current" | "failed" | "pending";
};

/** Human-readable progress derived from the operation's recorded state. */
export function operationSteps(operation: Operation): Step[] {
  const state = operation.state;
  const message = operation.message || undefined;
  if (operation.kind === OperationKind.ENROLL_SERVER) {
    return [
      { title: "Requested", status: "done" },
      {
        title: "Setting up server",
        detail: message,
        status:
          state === OperationState.SUCCEEDED
            ? "done"
            : state === OperationState.FAILED
              ? "failed"
              : "current",
      },
    ];
  }
  const rollback = operation.kind === OperationKind.ROLLBACK;
  const prepared = operation.releaseId !== "";
  const waiting = state === OperationState.WAITING_FOR_BUILD;
  const superseded = state === OperationState.SUPERSEDED;
  const failedBeforeRelease =
    !prepared && (state === OperationState.FAILED || superseded);
  const steps: Step[] = [
    { title: "Requested", status: "done" },
    {
      title: rollback ? "Release selected" : "Images ready",
      detail:
        waiting || failedBeforeRelease
          ? message
          : prepared
            ? undefined
            : "Waiting for the commit’s images.",
      status: prepared
        ? "done"
        : failedBeforeRelease && !superseded
          ? "failed"
          : superseded
            ? "pending"
            : "current",
    },
  ];
  const deploying: Step = { title: "Deploying", status: "pending" };
  const result: Step = { title: "Live", status: "pending" };
  switch (state) {
    case OperationState.QUEUED:
      deploying.status = prepared ? "current" : "pending";
      deploying.detail = prepared ? "Queued behind other work." : undefined;
      break;
    case OperationState.RUNNING:
      deploying.status = "current";
      deploying.detail = message;
      break;
    case OperationState.SUCCEEDED:
      deploying.status = "done";
      result.status = "done";
      break;
    case OperationState.FAILED:
      if (prepared) {
        deploying.status = "failed";
        deploying.detail = operation.diagnostics.length
          ? operation.diagnostics
              .map(
                (item) =>
                  `${item.service} ${item.state || "failed"}${item.state === "exited" ? ` with code ${item.exitCode}` : ""}`,
              )
              .join(", ")
          : message === "Operation failed."
            ? undefined
            : message;
        result.title = "Not deployed";
        result.status = "failed";
      }
      break;
    case OperationState.RECOVERING:
      deploying.status = "failed";
      result.title = "Restoring previous release";
      result.status = "current";
      result.detail = message;
      break;
    case OperationState.RECOVERED:
      deploying.status = "failed";
      result.title = "Previous release restored";
      result.status = "done";
      result.detail = message;
      break;
    case OperationState.NEEDS_ATTENTION:
      deploying.status = "failed";
      result.title = "Needs attention";
      result.status = "current";
      result.detail = message;
      break;
    case OperationState.SUPERSEDED:
      result.title = "Superseded";
      result.detail = message;
      break;
  }
  return [...steps, deploying, result];
}
