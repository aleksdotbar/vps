package deployment

import "github.com/aleksdotbar/vps/panel/internal/routes"

// A neutral receipt carries no active-release authority. Only a domainless
// candidate and domainless retained previous release may rely on native app
// proof alone. Callers separately verify exact containers/publications/current.
func routeReceiptMatches(receipt routes.RuntimeReceipt, candidate staged, previous *releaseMaterial, restored bool) bool {
	identity := candidate.Manifest.Identity
	if receipt.OperationID != identity.OperationID || receipt.ReleaseID != identity.ReleaseID || receipt.EnvironmentID != identity.EnvironmentID || receipt.RouteSnapshotSHA256 != candidate.Manifest.RouteSnapshotSHA256 {
		return false
	}
	if receipt.Phase == routes.RuntimeNeutral {
		return receipt.ActiveReleaseID == "" && receipt.Revision == 0 && receipt.ConfigSHA256 == "" && len(candidate.Manifest.RouteSnapshot.Bindings) == 0 && (previous == nil || len(previous.Stage.Manifest.RouteSnapshot.Bindings) == 0)
	}
	if !validHash(receipt.ConfigSHA256) {
		return false
	}
	if restored {
		return receipt.Phase == routes.RuntimeRestored && receipt.ActiveReleaseID == identity.ExpectedCurrentReleaseID
	}
	return receipt.Phase == routes.RuntimeReady && receipt.ActiveReleaseID == identity.ReleaseID
}
