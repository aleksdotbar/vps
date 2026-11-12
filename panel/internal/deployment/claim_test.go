package deployment

import "testing"

func TestRetainedClaimSurvivesGenerationChange(t *testing.T) {
	staged := claim{Identity: Identity{OperationID: "a"}, ManifestSHA256: "h", FinalSystem: "/nix/store/old-nixos-system"}
	current := claim{Identity: Identity{OperationID: "a"}, ManifestSHA256: "h", FinalSystem: "/nix/store/new-nixos-system"}
	if !staged.sameOperation(current) {
		t.Fatal("a rebuild after staging must not orphan the operation")
	}
	current.ManifestSHA256 = "other"
	if staged.sameOperation(current) {
		t.Fatal("a different manifest must not match")
	}
}
