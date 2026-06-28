// Package aclsource abstracts where the ACL YAML is loaded from — a local file
// or an S3-compatible object store. The Source re-fetches on every Fetch call
// so boot and SIGHUP both pull the current content without caching stale bytes.
package aclsource

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3 holds the S3 object location and credentials.
// When Bucket is empty the Source falls back to file mode.
type S3 struct {
	Endpoint, Bucket, Key, Region, AccessKeyID, SecretAccessKey string
	UsePathStyle                                                 bool
}

// Source knows how to fetch raw ACL YAML bytes from either a file or S3.
type Source struct {
	file string
	s3   S3
}

// New returns a Source. If s3.Bucket is non-empty, Fetch retrieves from S3;
// otherwise it reads from the file at path file.
func New(file string, s3cfg S3) *Source {
	return &Source{file: file, s3: s3cfg}
}

// Fetch returns current ACL YAML bytes and a short description of the origin
// (e.g. "file:/app/acl.yaml" or "s3:my-bucket/acl.yaml").
// Every call re-fetches — callers must supply an appropriate context deadline.
func (s *Source) Fetch(ctx context.Context) ([]byte, string, error) {
	if s.s3.Bucket == "" {
		data, err := os.ReadFile(s.file)
		if err != nil {
			return nil, "", fmt.Errorf("reading ACL file %q: %w", s.file, err)
		}
		return data, "file:" + s.file, nil
	}
	return s.fetchS3(ctx)
}

func (s *Source) fetchS3(ctx context.Context) ([]byte, string, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(s.s3.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(s.s3.AccessKeyID, s.s3.SecretAccessKey, ""),
		),
	)
	if err != nil {
		return nil, "", fmt.Errorf("aws config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(s.s3.Endpoint)
		o.UsePathStyle = s.s3.UsePathStyle
	})

	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.s3.Bucket),
		Key:    aws.String(s.s3.Key),
	})
	if err != nil {
		return nil, "", fmt.Errorf("s3 GetObject %s/%s: %w", s.s3.Bucket, s.s3.Key, err)
	}
	defer out.Body.Close()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, "", fmt.Errorf("reading s3 object body: %w", err)
	}

	return data, fmt.Sprintf("s3:%s/%s", s.s3.Bucket, s.s3.Key), nil
}
