package cloudflareflow

import (
	"context"
	"time"

	"github.com/cloudflare/cloudflare-go/v7/dns"

	"github.com/aleksdotbar/vps/panel/internal/routes"
	"github.com/aleksdotbar/vps/panel/internal/store"
)

func dnsComment(snapshot routes.Snapshot, binding routes.Binding) string {
	// 79 ASCII characters, below the Free-plan limit of 100. This marker is
	// correlation data, not a secret or standalone ownership authorization.
	return "panel:" + snapshot.InstallationID + ":" + binding.ClaimID
}

func validDNSRecord(record *dns.RecordResponse, snapshot routes.Snapshot, binding routes.Binding, attempt store.DnsRecord) bool {
	return record != nil && idPattern.MatchString(record.ID) && (!attempt.RecordID.Valid || record.ID == attempt.RecordID.String) &&
		record.Type == dns.RecordResponseTypeCNAME && record.Name == binding.Hostname && record.Content == snapshot.TunnelID+".cfargotunnel.com" &&
		record.Proxied && record.TTL == 1 && record.Comment == dnsComment(snapshot, binding) && !record.PrivateRouting && !record.JSON.PrivateRouting.IsInvalid() &&
		!record.CreatedOn.IsZero() && !record.JSON.CreatedOn.IsInvalid() &&
		!record.CreatedOn.Before(time.UnixMilli(attempt.CreateAttemptedAt).Add(-5*time.Minute)) && !record.CreatedOn.After(time.UnixMilli(attempt.CreateAttemptedAt).Add(5*time.Minute)) && !record.CreatedOn.After(time.Now().Add(5*time.Minute))
}

func (d *DNSRecords) find(ctx context.Context, client *dns.RecordService, snapshot routes.Snapshot, binding routes.Binding, attempt *store.DnsRecord) (*dns.RecordResponse, error) {
	if attempt != nil && attempt.RecordID.Valid {
		var params dns.RecordGetParams
		params.ZoneID.Value, params.ZoneID.Present = binding.ZoneID, true
		record, err := client.Get(ctx, attempt.RecordID.String, params)
		if err != nil {
			return nil, ErrProvider
		}
		if !validDNSRecord(record, snapshot, binding, *attempt) {
			return nil, ErrDNSConflict
		}
	}
	var params dns.RecordListParams
	params.ZoneID.Value, params.ZoneID.Present = binding.ZoneID, true
	params.PerPage.Value, params.PerPage.Present = 2, true
	params.Page.Value, params.Page.Present = 1, true
	var name dns.RecordListParamsName
	name.Exact.Value, name.Exact.Present = binding.Hostname, true
	params.Name.Value, params.Name.Present = name, true
	// No type/content/comment filter: any foreign record at this exact name is
	// a conflict, including an otherwise identical unmanaged CNAME.
	page, err := client.List(ctx, params)
	if err != nil || page == nil || page.JSON.Result.IsNull() || page.JSON.Result.IsInvalid() {
		return nil, ErrProvider
	}
	if len(page.Result) == 0 {
		return nil, errDNSAbsent
	}
	if attempt == nil || len(page.Result) != 1 || !validDNSRecord(&page.Result[0], snapshot, binding, *attempt) {
		return nil, ErrDNSConflict
	}
	return &page.Result[0], nil
}

func (d *DNSRecords) create(ctx context.Context, client *dns.RecordService, snapshot routes.Snapshot, binding routes.Binding, attempt store.DnsRecord) (*dns.RecordResponse, error) {
	var body dns.CNAMERecordParam
	body.Name.Value, body.Name.Present = binding.Hostname, true
	body.Type.Value, body.Type.Present = dns.CNAMERecordTypeCNAME, true
	body.Content.Value, body.Content.Present = snapshot.TunnelID+".cfargotunnel.com", true
	body.TTL.Value, body.TTL.Present = 1, true
	body.Proxied.Value, body.Proxied.Present = true, true
	body.Comment.Value, body.Comment.Present = dnsComment(snapshot, binding), true
	var params dns.RecordNewParams
	params.ZoneID.Value, params.ZoneID.Present, params.Body = binding.ZoneID, true, body
	record, err := client.New(ctx, params)
	if err != nil || record == nil {
		return nil, ErrDNSUnknown
	}
	if !validDNSRecord(record, snapshot, binding, attempt) {
		return nil, ErrDNSConflict
	}
	return record, nil
}
