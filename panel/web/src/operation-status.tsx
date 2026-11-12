import { Badge, Loader } from "@cloudflare/kumo";

import {
  type Operation,
  OperationKind,
  OperationOutcome,
  OperationState,
} from "../../gen/ts/panel/v1/panel_pb";

const kinds: Record<OperationKind, string> = {
  [OperationKind.UNSPECIFIED]: "Operation",
  [OperationKind.ENROLL_SERVER]: "Server setup",
  [OperationKind.DEPLOY]: "Deployment",
  [OperationKind.ROLLBACK]: "Rollback",
};
type Variant = "neutral" | "info" | "success" | "error" | "warning";

const states: Record<OperationState, [string, Variant]> = {
  [OperationState.UNSPECIFIED]: ["Unknown", "neutral"],
  [OperationState.QUEUED]: ["Queued", "info"],
  [OperationState.RUNNING]: ["Deploying", "info"],
  [OperationState.SUCCEEDED]: ["Succeeded", "success"],
  [OperationState.FAILED]: ["Failed", "error"],
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
  const [label, variant] = states[value] ?? ["Unknown", "neutral"];
  return (
    <Badge variant={variant} icon={active ? <Loader size={12} /> : undefined}>
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
