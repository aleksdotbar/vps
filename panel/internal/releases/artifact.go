package releases

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

const MaxArtifactBytes = 64 << 10

var ErrInvalidArtifact = errors.New("invalid release artifact")

type Artifact struct {
	SchemaVersion int                        `json:"schema_version"`
	SourceSHA     string                     `json:"source_sha"`
	Services      map[string]ArtifactService `json:"services"`
}

type ArtifactService struct {
	ImageVariable string `json:"image_variable"`
	Image         string `json:"image"`
}

var (
	artifactSHA      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	artifactService  = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	artifactVariable = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	artifactImage    = regexp.MustCompile(`^ghcr\.io/[a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)+@sha256:[0-9a-f]{64}$`)
)

// ParseArtifact admits transport metadata only. The caller separately verifies
// provider authority, bounds the download/extraction and matches native Compose.
func ParseArtifact(data []byte, expectedSourceSHA string) (Artifact, error) {
	if len(data) > MaxArtifactBytes || !utf8.Valid(data) {
		return Artifact{}, invalidArtifact("metadata must be valid UTF-8 within 64 KiB")
	}
	if !artifactSHA.MatchString(expectedSourceSHA) {
		return Artifact{}, invalidArtifact("expected source must be an immutable SHA")
	}
	fields, err := artifactObject(data, 3)
	if err != nil {
		return Artifact{}, err
	}
	versionData, hasVersion := fields["schema_version"]
	sourceData, hasSource := fields["source_sha"]
	servicesData, hasServices := fields["services"]
	if len(fields) != 3 || !hasVersion || !hasSource || !hasServices {
		return Artifact{}, invalidArtifact("artifact fields must match schema")
	}

	var artifact Artifact
	if err := json.Unmarshal(versionData, &artifact.SchemaVersion); err != nil || artifact.SchemaVersion != 1 {
		return Artifact{}, invalidArtifact("unsupported schema version")
	}
	if err := json.Unmarshal(sourceData, &artifact.SourceSHA); err != nil || artifact.SourceSHA != expectedSourceSHA {
		return Artifact{}, invalidArtifact("source does not match expected immutable SHA")
	}
	services, err := artifactObject(servicesData, 64)
	if err != nil {
		return Artifact{}, err
	}
	if len(services) == 0 {
		return Artifact{}, invalidArtifact("services must contain 1 to 64 entries")
	}

	artifact.Services = make(map[string]ArtifactService, len(services))
	variables := make(map[string]struct{}, len(services))
	for name, serviceData := range services {
		if len(name) > 128 || !artifactService.MatchString(name) {
			return Artifact{}, invalidArtifact("invalid service name")
		}
		service, err := parseArtifactService(serviceData)
		if err != nil {
			return Artifact{}, err
		}
		if _, exists := variables[service.ImageVariable]; exists {
			return Artifact{}, invalidArtifact("image variables must be unique")
		}
		variables[service.ImageVariable] = struct{}{}
		artifact.Services[name] = service
	}
	return artifact, nil
}

func parseArtifactService(data []byte) (ArtifactService, error) {
	fields, err := artifactObject(data, 2)
	if err != nil {
		return ArtifactService{}, err
	}
	variableData, hasVariable := fields["image_variable"]
	imageData, hasImage := fields["image"]
	if len(fields) != 2 || !hasVariable || !hasImage {
		return ArtifactService{}, invalidArtifact("service fields must match schema")
	}
	var service ArtifactService
	if err := json.Unmarshal(variableData, &service.ImageVariable); err != nil || len(service.ImageVariable) > 64 || !artifactVariable.MatchString(service.ImageVariable) {
		return ArtifactService{}, invalidArtifact("invalid image variable")
	}
	if err := json.Unmarshal(imageData, &service.Image); err != nil || len(service.Image) > 512 || !artifactImage.MatchString(service.Image) {
		return ArtifactService{}, invalidArtifact("image must be a GHCR digest reference")
	}
	return service, nil
}

func artifactObject(data []byte, maxFields int) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, invalidArtifact("expected JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, invalidArtifact("invalid JSON object")
		}
		name, ok := token.(string)
		if !ok {
			return nil, invalidArtifact("invalid JSON object key")
		}
		if _, exists := fields[name]; exists {
			return nil, invalidArtifact("duplicate JSON object key")
		}
		if len(fields) >= maxFields {
			return nil, invalidArtifact("too many JSON object fields")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, invalidArtifact("invalid JSON object value")
		}
		fields[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, invalidArtifact("invalid JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, invalidArtifact("trailing JSON data")
	}
	return fields, nil
}

func invalidArtifact(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidArtifact, reason)
}
