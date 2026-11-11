import {
  type Operation,
  OperationKind,
  OperationOutcome,
  OperationState,
} from "../../gen/ts/panel/v1/panel_pb";

const kinds: Record<OperationKind, string> = {
  [OperationKind.UNSPECIFIED]: "Unknown operation",
  [OperationKind.ENROLL_SERVER]: "Server enrollment",
  [OperationKind.DEPLOY]: "Deployment",
  [OperationKind.ROLLBACK]: "Rollback",
};
const states: Record<OperationState, string> = {
  [OperationState.UNSPECIFIED]: "Unknown status",
  [OperationState.QUEUED]: "Queued",
  [OperationState.RUNNING]: "Running",
  [OperationState.SUCCEEDED]: "Succeeded",
  [OperationState.FAILED]: "Failed",
  [OperationState.RECOVERING]: "Recovering",
  [OperationState.RECOVERED]: "Recovered",
  [OperationState.SUPERSEDED]: "Superseded",
  [OperationState.NEEDS_ATTENTION]: "Needs attention",
  [OperationState.WAITING_FOR_BUILD]: "Waiting for build",
};
const outcomes: Record<OperationOutcome, string> = {
  [OperationOutcome.UNSPECIFIED]: "Unknown outcome",
  [OperationOutcome.PENDING]: "Pending",
  [OperationOutcome.CONFIRMED_SUCCESS]: "Confirmed success",
  [OperationOutcome.CONFIRMED_FAILURE]: "Confirmed failure",
  [OperationOutcome.PARTIAL]: "Partially applied",
  [OperationOutcome.UNKNOWN]: "Unknown remote outcome",
};

export function operationKind(value: OperationKind) {
  return kinds[value] ?? `Unsupported operation (${value})`;
}

export function operationOutcome(value: OperationOutcome) {
  return outcomes[value] ?? `Unsupported outcome (${value})`;
}

export function OperationStatus({ value }: { value: OperationState }) {
  return (
    <span className="status">
      {states[value] ?? `Unsupported status (${value})`}
    </span>
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
