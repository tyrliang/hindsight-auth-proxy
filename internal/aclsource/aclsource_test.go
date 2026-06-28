package aclsource_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"main/internal/aclsource"
)

const testYAML = "admins:\n  - admin@example.com\nshared: []\nteams: {}\nusers: {}\n"

func TestFetch_FileMode(t *testing.T) {
	f := filepath.Join(t.TempDir(), "acl.yaml")
	if err := os.WriteFile(f, []byte(testYAML), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	src := aclsource.New(f, aclsource.S3{})
	data, desc, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(data) != testYAML {
		t.Errorf("data mismatch: got %q, want %q", data, testYAML)
	}
	if desc != "file:"+f {
		t.Errorf("desc mismatch: got %q, want %q", desc, "file:"+f)
	}
}

func TestFetch_FileMode_Missing(t *testing.T) {
	src := aclsource.New("/nonexistent/acl.yaml", aclsource.S3{})
	_, _, err := src.Fetch(context.Background())
	if err == nil {
		t.Error("Fetch of missing file should return error")
	}
}

// TestFetch_S3Mode starts a minimal path-style S3 server via httptest and verifies
// that the aws-sdk GetObject wiring delivers the YAML body correctly.
func TestFetch_S3Mode(t *testing.T) {
	bucket := "acl"
	key := "acl.yaml"
	want := testYAML

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path-style: /<bucket>/<key>
		expected := "/" + bucket + "/" + key
		if r.Method != http.MethodGet || r.URL.Path != expected {
			t.Logf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("ETag", `"abc123"`)
		w.Header().Set("Last-Modified", "Mon, 01 Jan 2024 00:00:00 GMT")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(want)) //nolint:errcheck
	}))
	defer srv.Close()

	src := aclsource.New("", aclsource.S3{
		Endpoint:        srv.URL,
		Bucket:          bucket,
		Key:             key,
		Region:          "us-east-1",
		AccessKeyID:     "test",
		SecretAccessKey: "test",
		UsePathStyle:    true,
	})

	data, desc, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch S3: %v", err)
	}
	if string(data) != want {
		t.Errorf("data mismatch: got %q, want %q", data, want)
	}
	if !strings.HasPrefix(desc, "s3:") {
		t.Errorf("desc should start with 's3:': got %q", desc)
	}
}
