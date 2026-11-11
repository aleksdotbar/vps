package operations

import (
	"context"
	"database/sql"

	"github.com/riverqueue/river"
)

// TunnelArgs carries a public database identity, never runtime credentials.
type TunnelArgs struct {
	ServerID string `json:"server_id"`
}

func (TunnelArgs) Kind() string { return "cloudflare_tunnel" }
func (r *Runtime) EnqueueTunnel(ctx context.Context, tx *sql.Tx, serverID string) (int64, error) {
	result, err := r.client.InsertTx(ctx, tx, TunnelArgs{ServerID: serverID}, &river.InsertOpts{Queue: queueName, MaxAttempts: MaxAttempts, UniqueOpts: DiscoveryUnique()})
	if err != nil {
		return 0, err
	}
	return result.Job.ID, nil
}
