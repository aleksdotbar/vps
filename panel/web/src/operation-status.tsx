import {
  type Operation,
  OperationKind,
  OperationOutcome,
  OperationState,
} from "../../gen/ts/panel/v1/panel_pb";
import { StatusBadge, type Tone } from "./components";

const kinds: Record<OperationKind, string> = {
  [OperationKind.UNSPECIFIED]: "Operation",
  [OperationKind.ENROLL_SERVER]: "Server setup",
  [OperationKind.DEPLOY]: "Deployment",
  [OperationKind.ROLLBACK]: "Rollback",
};

const states: Record<OperationState, [string, Tone]> = {
  [OperationState.UNSPECIFIED]: ["Unknown", "neutral"],
  [OperationState.QUEUED]: ["Queued", "neutral"],
  [OperationState.RUNNING]: ["Deploying", "neutral"],
  [OperationState.SUCCEEDED]: ["Succeeded", "success"],
  [OperationState.FAILED]: ["Failed", "danger"],
  [OperationState.RECOVERING]: ["Restoring", "warning"],
  [OperationState.RECOVERED]: ["Rolled back", "warning"],
  [OperationState.SUPERSEDED]: ["Superseded", "neutral"],
  [OperationState.NEEDS_ATTENTION]: ["Needs attention", "warning"],
  [OperationState.WAITING_FOR_BUILD]: ["Waiting for build", "neutral"],
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
  return <StatusBadge tone={active ? "pending" : tone}>{label}</StatusBadge>;
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
