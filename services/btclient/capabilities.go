package btclient

import (
	"context"
	"errors"
)

var ErrVersionUnsupported = errors.New("version_unsupported: qBittorrent protocol profile has not been verified")

// CheckQBProfile accepts only the source-audited 5.2.3 / Web API 2.15.1 pair.
// Supporting another version requires capability tests, not a guessed version range.
func (c *Client) CheckQBProfile(ctx context.Context) error {
	v, a, err := c.QBVersion(ctx)
	if err != nil {
		return err
	}
	if (v != "v5.2.3" && v != "5.2.3") || a != "2.15.1" {
		return ErrVersionUnsupported
	}
	return nil
}
