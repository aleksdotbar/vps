package compose

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func imageTree(t *testing.T, base, override string) string {
	t.Helper()
	tree := t.TempDir()
	for name, data := range map[string]string{"compose.yaml": base, "compose.staging.yaml": override} {
		if err := os.WriteFile(filepath.Join(tree, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return tree
}

func TestImagesReadsGitSHATaggedRepositories(t *testing.T) {
	base, err := os.ReadFile("../../../examples/stateless/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	images, err := Images(imageTree(t, string(base), "services:\n  app:\n    environment:\n      LOG_LEVEL: debug\n"), "compose.staging.yaml")
	if err != nil || images["app"] != "ghcr.io/example/stateless-app" || images["worker"] != "ghcr.io/example/stateless-worker" || len(images) != 2 {
		t.Fatalf("example images not read: %v %v", images, err)
	}
	override := "services:\n  app:\n    image: ghcr.io/example/other:${GIT_SHA}\n"
	images, err = Images(imageTree(t, string(base), override), "compose.staging.yaml")
	if err != nil || images["app"] != "ghcr.io/example/other" {
		t.Fatalf("override image not used: %v %v", images, err)
	}
}

func TestImagesRejectsNonConventionalReferences(t *testing.T) {
	for _, image := range []string{
		"ghcr.io/example/app:latest",
		"ghcr.io/example/app@sha256:" + "0000000000000000000000000000000000000000000000000000000000000000",
		"docker.io/example/app:${GIT_SHA}",
		"ghcr.io/Example/app:${GIT_SHA}",
		"ghcr.io/example/app:${APP_TAG}",
		"ghcr.io/example/app:${GIT_SHA}-suffix",
		"${GIT_SHA}",
	} {
		base := "services:\n  app:\n    image: " + image + "\n"
		if _, err := Images(imageTree(t, base, "services: {}\n"), "compose.staging.yaml"); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("image %q accepted: %v", image, err)
		}
	}
	missing := "services:\n  app:\n    image: ghcr.io/example/app:${GIT_SHA}\n  sidecar:\n    user: \"1:1\"\n"
	if _, err := Images(imageTree(t, missing, "services: {}\n"), "compose.staging.yaml"); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("service without image accepted: %v", err)
	}
}
