package objectstore

import (
	"context"
	"fmt"

	"github.com/hyscaler/qavia/api/internal/settings"
)

// SettingsReader is the slice of the settings service this package needs.
type SettingsReader interface {
	String(ctx context.Context, key string, target settings.Target) (string, error)
	Secret(ctx context.Context, key string, target settings.Target) (plaintext string, found bool, err error)
}

// FromSettings builds the configured driver.
//
// Driver selection lives here rather than in each main.go so the API and the
// worker cannot end up pointed at different storage, which would be a data loss
// bug that only shows up as "the file the job wrote is not there".
//
// The driver is chosen once, at boot, which is why storage.provider is declared
// restart-required: swapping the store under a running process would leave the
// objects already written unreachable through the new one.
func FromSettings(ctx context.Context, reader SettingsReader) (Store, error) {
	global := settings.Target{}

	provider, err := reader.String(ctx, "storage.provider", global)
	if err != nil {
		return nil, err
	}

	switch provider {
	case LocalID:
		root, err := reader.String(ctx, "storage.local_path", global)
		if err != nil {
			return nil, err
		}
		return NewLocal(root)

	case MinIOID, S3ID:
		cfg := S3Config{Driver: provider}

		if cfg.Endpoint, err = reader.String(ctx, "storage.endpoint", global); err != nil {
			return nil, err
		}
		if cfg.Region, err = reader.String(ctx, "storage.region", global); err != nil {
			return nil, err
		}
		if cfg.Bucket, err = reader.String(ctx, "storage.bucket", global); err != nil {
			return nil, err
		}

		// Absent keys are not an error: an EC2 instance role or a MinIO in
		// anonymous mode is a working configuration, and the credential chain
		// handles both.
		if cfg.AccessKey, _, err = reader.Secret(ctx, "storage.access_key", global); err != nil {
			return nil, err
		}
		if cfg.SecretKey, _, err = reader.Secret(ctx, "storage.secret_key", global); err != nil {
			return nil, err
		}

		return NewS3(ctx, cfg)

	default:
		return nil, fmt.Errorf(
			"objectstore: storage.provider is %q, which is not one of %s, %s, %s",
			provider, LocalID, MinIOID, S3ID)
	}
}
