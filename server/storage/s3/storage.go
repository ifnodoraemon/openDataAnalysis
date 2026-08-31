package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/ifnodoraemon/openDataAnalysis/storage"
)

type Storage struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	bucket        string
}

type Config struct {
	Endpoint       string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	ForcePathStyle bool
}

func New(ctx context.Context, cfg Config) (*Storage, error) {
	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		if cfg.Endpoint != "" {
			return aws.Endpoint{
				PartitionID:       "aws",
				URL:               cfg.Endpoint,
				SigningRegion:     cfg.Region,
				HostnameImmutable: cfg.ForcePathStyle,
			}, nil
		}
		return aws.Endpoint{}, &aws.EndpointNotFoundError{}
	})

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
		awsconfig.WithEndpointResolverWithOptions(customResolver),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.ForcePathStyle
	})

	// Fail fast when the configured bucket does not exist instead of
	// surfacing raw S3 errors on the first upload at runtime.
	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(cfg.Bucket),
	}); err != nil {
		return nil, fmt.Errorf("s3 bucket %q is not accessible (create it before starting): %w", cfg.Bucket, err)
	}

	presignClient := s3.NewPresignClient(client)

	return &Storage{
		client:        client,
		presignClient: presignClient,
		bucket:        cfg.Bucket,
	}, nil
}

func (s *Storage) Put(ctx context.Context, req storage.PutObjectRequest) (*storage.StoredObject, error) {
	putInput := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(req.Key),
		Body:        req.Body,
		ContentType: aws.String(req.ContentType),
	}
	if req.Size > 0 {
		putInput.ContentLength = aws.Int64(req.Size)
	}
	if len(req.Metadata) > 0 {
		putInput.Metadata = req.Metadata
	}

	out, err := s.client.PutObject(ctx, putInput)
	if err != nil {
		return nil, fmt.Errorf("s3 PutObject failed key=%s: %w", req.Key, err)
	}

	etag := ""
	if out.ETag != nil {
		etag = *out.ETag
	}
	versionID := ""
	if out.VersionId != nil {
		versionID = *out.VersionId
	}

	return &storage.StoredObject{
		Provider:    "s3",
		Bucket:      s.bucket,
		Key:         req.Key,
		ETag:        etag,
		VersionID:   versionID,
		Size:        req.Size,
		ContentType: req.ContentType,
	}, nil
}

func (s *Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 GetObject failed key=%s: %w", key, err)
	}
	return out.Body, nil
}

func (s *Storage) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3 DeleteObject failed key=%s: %w", key, err)
	}
	return nil
}

func (s *Storage) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}
	var nsk *types.NotFound
	if errors.As(err, &nsk) {
		return false, nil
	}
	var responseErr *smithyhttp.ResponseError
	if errors.As(err, &responseErr) && responseErr.HTTPStatusCode() == http.StatusNotFound {
		return false, nil
	}
	return false, fmt.Errorf("s3 HeadObject failed key=%s: %w", key, err)
}

func (s *Storage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	req, err := s.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("s3 PresignGetObject failed key=%s: %w", key, err)
	}
	return req.URL, nil
}
