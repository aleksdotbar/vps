package githubflow

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"net/url"
	"testing"

	"github.com/google/go-github/v93/github"

	"github.com/aleksdotbar/vps/panel/internal/store"
)

func TestArtifactDownloadRejectsPrivateTranslatedAndNonProviderOrigins(t *testing.T) {
	for _, test := range []struct {
		value   string
		allowed bool
	}{{"20.60.1.1", true}, {"2603:1030:20e:3::23", true}, {"127.0.0.1", false}, {"10.0.0.1", false}, {"100.64.1.2", false}, {"169.254.169.254", false}, {"::ffff:127.0.0.1", false}, {"fec0::1", false}, {"64:ff9b::a00:1", false}, {"64:ff9b:1::a00:1", false}, {"100::1", false}, {"2001::1", false}, {"2002:a00:1::1", false}, {"3fff::1", false}, {"fc00::1", false}, {"fe80::1", false}} {
		if got := publicAddress(netip.MustParseAddr(test.value)); got != test.allowed {
			t.Fatalf("public address %s=%t", test.value, got)
		}
	}
	for _, value := range []string{"http://artifacts.actions.githubusercontent.com/file", "https://localhost/file", "https://artifacts.actions.githubusercontent.com:443/file", "https://user@artifacts.actions.githubusercontent.com/file", "https://blob.core.windows.net/file", "https://artifacts.actions.githubusercontent.com.evil.test/file"} {
		parsed, err := url.Parse(value)
		if err != nil {
			t.Fatal(err)
		}
		if ArtifactURL(parsed) {
			t.Fatalf("unsafe artifact origin accepted: %s", value)
		}
	}
}
func TestArtifactArchiveBindsRawDigestAndSoleRootMetadata(t *testing.T) {
	for _, names := range [][]string{{"release.json"}, {"../release.json"}, {"release.json", "extra"}, {"release.json", "release.json"}} {
		var buffer bytes.Buffer
		writer := zip.NewWriter(&buffer)
		for _, name := range names {
			file, err := writer.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte(`{}`)); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(buffer.Bytes())
		digest := "sha256:" + hex.EncodeToString(hash[:])
		_, err := decodeArchive(buffer.Bytes(), digest)
		valid := len(names) == 1 && names[0] == "release.json"
		if (err == nil) != valid {
			t.Fatalf("archive paths%v admitted=%t", names, err == nil)
		}
		if _, err := decodeArchive(buffer.Bytes(), "sha256:"+string(bytes.Repeat([]byte{'0'}, 64))); err == nil {
			t.Fatal("unbound provider digest accepted")
		}
	}
}
func TestRunEligibilityRejectsWrongAuthorityBeforeArtifacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*github.WorkflowRun)
	}{
		{"fork head", func(run *github.WorkflowRun) { run.HeadRepository = &github.Repository{ID: new(int64(8))} }},
		{"wrong repository", func(run *github.WorkflowRun) { run.Repository = &github.Repository{ID: new(int64(8))} }},
		{"wrong workflow", func(run *github.WorkflowRun) { run.WorkflowID = new(int64(8)) }},
		{"wrong branch", func(run *github.WorkflowRun) { run.HeadBranch = new("release") }},
		{"wrong source", func(run *github.WorkflowRun) { run.HeadSHA = new("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") }},
		{"pull request", func(run *github.WorkflowRun) { run.Event = new("pull_request") }},
		{"wrong path", func(run *github.WorkflowRun) { run.Path = new(".github/workflows/untrusted.yaml") }},
	} {
		run := fixtureRun(7, "push")
		test.change(run)
		if eligibleRun(run, store.Project{RepositoryID: 1, WorkflowID: 2}, "main", fixtureSHA, true) {
			t.Fatalf("untrusted %s accepted", test.name)
		}
	}
}
