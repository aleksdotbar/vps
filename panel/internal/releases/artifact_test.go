package releases

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const artifactSource = "0123456789abcdef0123456789abcdef01234567"

const validArtifact = `{
  "schema_version": 1,
  "source_sha": "0123456789abcdef0123456789abcdef01234567",
  "services": {
    "app": {
      "image_variable": "APP_IMAGE",
      "image": "ghcr.io/example/stateless-app@sha256:1111111111111111111111111111111111111111111111111111111111111111"
    },
    "worker": {
      "image_variable": "WORKER_IMAGE",
      "image": "ghcr.io/example/stateless-worker@sha256:2222222222222222222222222222222222222222222222222222222222222222"
    }
  }
}`

func TestParseArtifactPreservesServiceImageBindings(t *testing.T) {
	artifact, err := ParseArtifact([]byte(validArtifact), artifactSource)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.SchemaVersion != 1 || artifact.SourceSHA != artifactSource || len(artifact.Services) != 2 {
		t.Fatalf("artifact identity or services changed: %+v", artifact)
	}
	if artifact.Services["app"].ImageVariable != "APP_IMAGE" || artifact.Services["worker"].ImageVariable != "WORKER_IMAGE" || artifact.Services["app"].Image != "ghcr.io/example/stateless-app@sha256:"+strings.Repeat("1", 64) || artifact.Services["worker"].Image != "ghcr.io/example/stateless-worker@sha256:"+strings.Repeat("2", 64) {
		t.Fatalf("service image bindings changed: %+v", artifact.Services)
	}
}

func TestParseArtifactRejectsAmbiguousOrUnsafeMetadata(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"duplicate root field", strings.Replace(validArtifact, `"schema_version": 1`, `"schema_version": 1, "schema_version": 1`, 1)},
		{"escaped duplicate root field", strings.Replace(validArtifact, `"source_sha":`, `"source_sha":"`+artifactSource+`", "\u0073ource_sha":`, 1)},
		{"duplicate service", strings.Replace(validArtifact, `"worker":`, `"app":`, 1)},
		{"duplicate service image", strings.Replace(validArtifact, `"image_variable": "APP_IMAGE"`, `"image_variable": "APP_IMAGE", "image_variable": "OTHER_IMAGE"`, 1)},
		{"case alias", strings.Replace(validArtifact, `"source_sha":`, `"SOURCE_SHA":`, 1)},
		{"unknown field", strings.Replace(validArtifact, `"schema_version": 1`, `"schema_version": 1, "credential": "must-stay-redacted"`, 1)},
		{"unknown service field", strings.Replace(validArtifact, `"image_variable": "APP_IMAGE"`, `"image_variable": "APP_IMAGE", "privileged": true`, 1)},
		{"wrong schema type", strings.Replace(validArtifact, `"schema_version": 1`, `"schema_version": "1"`, 1)},
		{"unsupported schema", strings.Replace(validArtifact, `"schema_version": 1`, `"schema_version": 2`, 1)},
		{"null image", strings.Replace(validArtifact, `"image_variable": "APP_IMAGE"`, `"image_variable": null`, 1)},
		{"missing image", strings.Replace(validArtifact, `"image": "ghcr.io/example/stateless-app`, `"other": "ghcr.io/example/stateless-app`, 1)},
		{"shared image variable", strings.Replace(validArtifact, `"WORKER_IMAGE"`, `"APP_IMAGE"`, 1)},
		{"mutable image", strings.Replace(validArtifact, `@sha256:`+strings.Repeat("1", 64), `:latest`, 1)},
		{"foreign registry", strings.Replace(validArtifact, `ghcr.io/`, `ghcr.io.attacker.test/`, 1)},
		{"unsafe service name", strings.Replace(validArtifact, `"app":`, `"../../app":`, 1)},
		{"trailing object", validArtifact + `{}`},
		{"trailing non-JSON", validArtifact + ` garbage`},
		{"null root", `null`},
		{"array root", `[` + validArtifact + `]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact, err := ParseArtifact([]byte(test.data), artifactSource)
			if !errors.Is(err, ErrInvalidArtifact) {
				t.Fatalf("unsafe metadata accepted (%s): %v", test.name, err)
			}
			if artifact.SchemaVersion != 0 || artifact.SourceSHA != "" || artifact.Services != nil {
				t.Fatalf("failure returned a partially admitted artifact (%s): %+v", test.name, artifact)
			}
			if strings.Contains(err.Error(), "must-stay-redacted") || strings.Contains(err.Error(), "../../app") {
				t.Fatalf("error exposed supplied metadata (%s): %v", test.name, err)
			}
		})
	}
}

func TestParseArtifactEnforcesSourceAndWorkBounds(t *testing.T) {
	for _, expectedSource := range []string{strings.Repeat("a", 40), "main"} {
		if _, err := ParseArtifact([]byte(validArtifact), expectedSource); !errors.Is(err, ErrInvalidArtifact) {
			t.Fatalf("unbound source accepted for expected SHA %q: %v", expectedSource, err)
		}
	}

	for _, size := range []int{MaxArtifactBytes, MaxArtifactBytes + 1} {
		data := validArtifact + strings.Repeat(" ", size-len(validArtifact))
		_, err := ParseArtifact([]byte(data), artifactSource)
		if (size == MaxArtifactBytes && err != nil) || (size > MaxArtifactBytes && !errors.Is(err, ErrInvalidArtifact)) {
			t.Fatalf("incorrect byte admission at %d bytes: %v", size, err)
		}
	}

	for _, count := range []int{0, 64, 65} {
		services := make([]string, count)
		for index := range services {
			services[index] = fmt.Sprintf(`"service%d":{"image_variable":"IMAGE_%d","image":"ghcr.io/example/app@sha256:%s"}`, index, index, strings.Repeat("a", 64))
		}
		data := fmt.Sprintf(`{"schema_version":1,"source_sha":%q,"services":{%s}}`, artifactSource, strings.Join(services, ","))
		_, err := ParseArtifact([]byte(data), artifactSource)
		if (count == 64 && err != nil) || (count != 64 && !errors.Is(err, ErrInvalidArtifact)) {
			t.Fatalf("incorrect service-count admission at %d services: %v", count, err)
		}
	}
}

func TestParseArtifactAcceptsResolvedSetWithoutImageVariables(t *testing.T) {
	data := `{"schema_version":1,"source_sha":"` + artifactSource + `","services":{"app":{"image":"ghcr.io/example/app@sha256:` + strings.Repeat("1", 64) + `"},"worker":{"image":"ghcr.io/example/worker@sha256:` + strings.Repeat("2", 64) + `"}}}`
	artifact, err := ParseArtifact([]byte(data), artifactSource)
	if err != nil || artifact.Services["app"].ImageVariable != "" || artifact.Services["worker"].Image != "ghcr.io/example/worker@sha256:"+strings.Repeat("2", 64) {
		t.Fatalf("resolved digest set rejected: %v", err)
	}
}

func TestParseArtifactServiceRejectsExtraFields(t *testing.T) {
	image := `"ghcr.io/example/app@sha256:` + strings.Repeat("1", 64) + `"`
	for _, data := range []string{`{"image":` + image + `,"x":1}`, `{"image":` + image + `,"x":1,"y":2}`} {
		if _, err := parseArtifactService([]byte(data)); err == nil {
			t.Fatalf("extra fields accepted: %s", data)
		}
	}
	if _, err := parseArtifactService([]byte(`{"image":` + image + `,"image_variable":"APP_IMAGE"}`)); err != nil {
		t.Fatal(err)
	}
}
