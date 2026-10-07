package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// Driver IDs for the two S3-compatible drivers. They differ by endpoint and
// addressing style, not by code, which is why one type serves both.
const (
	MinIOID = "minio"
	S3ID    = "s3"
)

// S3Config is what the driver needs from settings.
type S3Config struct {
	// Driver is minio or s3. It selects the ID and the addressing style: MinIO is
	// addressed path-style because a self-hosted deployment rarely has the wildcard
	// DNS that virtual-host addressing needs.
	Driver string

	Endpoint string
	Region   string
	Bucket   string

	AccessKey string
	SecretKey string
}

// S3 stores objects in any S3-compatible bucket.
//
// MinIO and S3 are the same client with a different endpoint (tech-stack.md 6),
// so there is one implementation and no vendor branch below this constructor.
type S3 struct {
	id      string
	bucket  string
	client  *s3.Client
	presign *s3.PresignClient
}

// NewS3 builds the client.
//
// Static credentials come from settings, not from the environment or an instance
// profile: the platform is configured through the UI, and a driver that silently
// picked up ambient AWS credentials would make "what is this writing to" a
// question nobody could answer from the settings screen. Where the keys are left
// empty the default chain is used, which is how an instance role is opted into
// deliberately.
func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("objectstore: storage.bucket is empty")
	}

	id := S3ID
	pathStyle := false
	if cfg.Driver == MinIOID {
		id = MinIOID
		pathStyle = true
	}

	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		options = append(options, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("build object store client: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if endpoint := strings.TrimSpace(cfg.Endpoint); endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		o.UsePathStyle = pathStyle
	})

	return &S3{
		id:      id,
		bucket:  cfg.Bucket,
		client:  client,
		presign: s3.NewPresignClient(client),
	}, nil
}

func (s *S3) ID() string { return s.id }

// Available reports whether the bucket answers.
//
// HeadBucket rather than a write: this is called on the readiness path, and a
// readiness probe that writes an object every few seconds is its own problem. The
// write-and-read-back check is Probe, which setup runs once.
func (s *S3) Available(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := s.client.HeadBucket(probeCtx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	return err == nil
}

// Put uploads to key.
//
// The SDK signs the payload, which needs either a known length over a seekable
// body or a full buffer. An upload arrives as a stream of unknown length, so a
// body that is not already seekable is spooled to a temporary file rather than
// into memory: a 2 GB archive must not become 2 GB of heap.
func (s *S3) Put(ctx context.Context, key string, r io.Reader, opts PutOptions) (Object, error) {
	if err := ValidateKey(key); err != nil {
		return Object{}, err
	}

	body, size, cleanup, err := seekable(r, opts.Size)
	if err != nil {
		return Object{}, fmt.Errorf("prepare upload for %q: %w", key, err)
	}
	defer cleanup()

	input := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
	}
	if opts.ContentType != "" {
		input.ContentType = aws.String(opts.ContentType)
	}

	if _, err := s.client.PutObject(ctx, input); err != nil {
		return Object{}, fmt.Errorf("upload %q: %w", key, err)
	}

	return Object{
		Key:         key,
		Size:        size,
		ContentType: opts.ContentType,
		ModTime:     time.Now().UTC(),
	}, nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNoSuchKey(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("download %q: %w", key, err)
	}
	return out.Body, nil
}

func (s *S3) Stat(ctx context.Context, key string) (Object, error) {
	if err := ValidateKey(key); err != nil {
		return Object{}, err
	}

	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNoSuchKey(err) {
			return Object{}, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return Object{}, fmt.Errorf("stat %q: %w", key, err)
	}

	object := Object{Key: key}
	if out.ContentLength != nil {
		object.Size = *out.ContentLength
	}
	if out.ContentType != nil {
		object.ContentType = *out.ContentType
	}
	if out.LastModified != nil {
		object.ModTime = out.LastModified.UTC()
	}
	if out.ETag != nil {
		object.ETag = strings.Trim(*out.ETag, `"`)
	}
	return object, nil
}

// Delete removes the object. S3 reports deleting a missing key as a success,
// which is the behaviour retention and job retries need anyway.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}

	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("delete %q: %w", key, err)
	}
	return nil
}

// SignedURL hands the download straight to the object store, so a 500 MB trace
// does not stream through the API process.
func (s *S3) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}

	request, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("sign url for %q: %w", key, err)
	}
	return request.URL, nil
}

// seekable returns a body the signer can rewind, its length, and a cleanup.
func seekable(r io.Reader, size int64) (io.Reader, int64, func(), error) {
	noop := func() {}

	if seeker, ok := r.(io.ReadSeeker); ok {
		if size > 0 {
			return seeker, size, noop, nil
		}
		end, err := seeker.Seek(0, io.SeekEnd)
		if err == nil {
			if _, err := seeker.Seek(0, io.SeekStart); err == nil {
				return seeker, end, noop, nil
			}
		}
		// Fall through and spool: a reader that claims to seek and then refuses is
		// not worth trusting with a signed upload.
	}

	spool, err := os.CreateTemp("", "qavia-upload-*")
	if err != nil {
		return nil, 0, noop, err
	}
	// Both failures below are terminal for this upload or irrelevant after it, so
	// they are logged rather than returned.
	cleanup := func() {
		name := spool.Name()
		if err := spool.Close(); err != nil {
			slog.Warn("close upload spool", "path", name, "error", err)
		}
		if err := os.Remove(name); err != nil {
			slog.Warn("remove upload spool", "path", name, "error", err)
		}
	}

	written, err := io.Copy(spool, r)
	if err != nil {
		cleanup()
		return nil, 0, noop, err
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, 0, noop, err
	}
	return spool, written, cleanup, nil
}

// isNoSuchKey recognises a missing object across the two shapes the SDK reports
// it in: a typed NoSuchKey from GetObject, and a bare 404 API error from
// HeadObject, which has no response body to type.
func isNoSuchKey(err error) bool {
	var missing *types.NoSuchKey
	if errors.As(err, &missing) {
		return true
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}
