import { useParams, useSearch } from "@tanstack/react-router";

import { OperationDetail, Operations } from "./operations";
import { ReleaseDetail } from "./release";

export function OperationsRoute() {
  const search = useSearch({ from: "/_private/operations" });
  return (
    <Operations
      key={`${search.environment}:${search.server}:${search.cursor}`}
      {...search}
    />
  );
}

export function OperationRoute() {
  const { operationId } = useParams({
    from: "/_private/operations/$operationId",
  });
  return <OperationDetail key={operationId} id={operationId} />;
}

export function ReleaseRoute() {
  const { releaseId } = useParams({ from: "/_private/releases/$releaseId" });
  return <ReleaseDetail key={releaseId} id={releaseId} />;
}
