package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Store implements Store using an S3-compatible bucket.
type S3Store struct {
	client *s3.Client
	bucket string
	prefix string
}

// NewS3Store creates an S3-backed store using explicit credentials from Config.
// If Endpoint is non-empty, it is used as a custom endpoint (for DigitalOcean Spaces, MinIO, etc.).
func NewS3Store(cfg Config) (*S3Store, error) {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	var s3Opts []func(*s3.Options)
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		})
	}

	client := s3.New(s3.Options{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
	}, s3Opts...)

	return &S3Store{
		client: client,
		bucket: cfg.Bucket,
		prefix: strings.TrimRight(cfg.Prefix, "/"),
	}, nil
}

func (s *S3Store) key(key string) string {
	if s.prefix == "" {
		return key
	}
	return s.prefix + "/" + key
}

func (s *S3Store) Get(key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: get %q: %w", key, err)
	}
	defer out.Body.Close()

	return io.ReadAll(out.Body)
}

func (s *S3Store) Put(key string, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	return nil
}

func (s *S3Store) Delete(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// List all objects under this key (handles "directory" deletes)
	prefix := s.key(key)
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	var objects []types.ObjectIdentifier
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("storage: delete list %q: %w", key, err)
		}
		for _, obj := range page.Contents {
			objects = append(objects, types.ObjectIdentifier{Key: obj.Key})
		}
	}

	if len(objects) == 0 {
		// Try deleting exact key
		_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(prefix),
		})
		if err != nil {
			return fmt.Errorf("storage: delete %q: %w", key, err)
		}
		return nil
	}

	// Batch delete
	_, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(s.bucket),
		Delete: &types.Delete{Objects: objects},
	})
	if err != nil {
		return fmt.Errorf("storage: delete %q: %w", key, err)
	}
	return nil
}

func (s *S3Store) Exists(key string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.key(key)),
	})
	if err != nil {
		// Check if it's a not-found error
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, fmt.Errorf("storage: exists %q: %w", key, err)
	}
	return true, nil
}

func (s *S3Store) List(prefix string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fullPrefix := s.key(prefix)
	if !strings.HasSuffix(fullPrefix, "/") {
		fullPrefix += "/"
	}

	out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:    aws.String(s.bucket),
		Prefix:    aws.String(fullPrefix),
		Delimiter: aws.String("/"),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: list %q: %w", prefix, err)
	}

	var keys []string
	// Include "directories" (common prefixes)
	for _, cp := range out.CommonPrefixes {
		key := strings.TrimPrefix(*cp.Prefix, s.prefix+"/")
		key = strings.TrimRight(key, "/")
		keys = append(keys, key)
	}
	// Include files at this level
	for _, obj := range out.Contents {
		key := strings.TrimPrefix(*obj.Key, s.prefix+"/")
		keys = append(keys, key)
	}

	return keys, nil
}
