// This file implements the Cost & Usage Report ingestor: the preferred
// ports.CostIngestor source whenever per-resource attribution
// (CostIngestInput.ResourceLevel) is requested, because the CUR — unlike
// Cost Explorer, which is capped at two GroupBy dimensions and never
// exposes a resource id at all — carries one line per resource per hour
// with a lineItem/ResourceId column.
//
// There is no "cur" SDK client in this codebase's allowed dependency list
// (cur:DescribeReportDefinitions is not callable), so the report location
// is not auto-discovered from AWS's own report-definition API; it is read
// from cloud.AWSAccount.CURBucket/CURPrefix, which the onboarding flow
// populates when the customer points CloudOptix at their report. Finding
// the manifest is then a plain S3 walk: list every object under the
// prefix, keep the ones whose key ends in "-Manifest.json" or
// "Manifest.json", download and parse each, and keep the manifests whose
// billingPeriod overlaps CostIngestInput.Period.
//
// Two manifest shapes are handled — see parseManifest's doc comment for
// the fidelity difference between them.
//
// Every report part named in a kept manifest is streamed rather than
// buffered whole: GetObject's response body goes straight through a
// gzip.Reader into an encoding/csv.Reader that yields one row at a time,
// so a multi-gigabyte CUR part never sits fully in process memory.
package costing

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/awserr"
	awssts "github.com/udaykishore-resu/cloudoptix/internal/adapters/aws/sts"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cloud"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

type curS3API interface {
	HeadBucket(ctx context.Context, in *s3.HeadBucketInput, optFns ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	GetBucketLocation(ctx context.Context, in *s3.GetBucketLocationInput, optFns ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// CURIngestor implements ports.CostIngestor over a Cost & Usage Report
// published to S3.
type CURIngestor struct {
	newClient func(aws.Config) curS3API
	// regionalClient rebuilds the client for the CUR bucket's own region,
	// resolved via GetBucketLocation — the same pattern discovery/s3.go
	// uses and for the same reason: S3 object calls must be signed against
	// the bucket's actual region.
	regionalClient func(cfg aws.Config, region string) curS3API
}

var _ ports.CostIngestor = (*CURIngestor)(nil)

// NewCURIngestor builds the CUR ingestor.
func NewCURIngestor() *CURIngestor {
	newClient := func(cfg aws.Config) curS3API { return s3.NewFromConfig(cfg) }
	return &CURIngestor{
		newClient: newClient,
		regionalClient: func(cfg aws.Config, region string) curS3API {
			regional := cfg.Copy()
			regional.Region = region
			return newClient(regional)
		},
	}
}

func (c *CURIngestor) Source() string { return "cur" }

// Available reports whether the account has a CUR bucket configured and
// that bucket is reachable — it does not confirm a manifest exists there
// yet (a bucket can be configured before the first report has landed), so
// Fetch returning zero records is a legitimate, non-error outcome even when
// Available is true.
func (c *CURIngestor) Available(ctx context.Context, session ports.AWSSession, account cloud.AWSAccount) bool {
	if account.CURBucket == "" || session == nil {
		return false
	}
	cfg, err := awssts.FromSession(session, costExplorerRegion)
	if err != nil {
		return false
	}
	client := c.newClient(cfg)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(account.CURBucket)})
	return err == nil
}

// Fetch locates every CUR manifest under the account's configured
// bucket/prefix whose billing period overlaps in.Period, streams each
// manifest's report parts, and returns the parsed cost.Record lines.
func (c *CURIngestor) Fetch(ctx context.Context, in ports.CostIngestInput) ([]cost.Record, error) {
	if in.Account.CURBucket == "" {
		return nil, core.Invalid("costing: account has no CUR bucket configured")
	}
	cfg, err := awssts.FromSession(in.Session, costExplorerRegion)
	if err != nil {
		return nil, err
	}
	client := c.newClient(cfg)

	region, err := c.bucketRegion(ctx, client, in.Account.CURBucket)
	if err != nil {
		return nil, awserr.Translate(err, "s3", "GetBucketLocation", "s3:GetBucketLocation")
	}
	regional := c.regionalClient(cfg, region)

	manifestKeys, err := c.listManifests(ctx, regional, in.Account.CURBucket, in.Account.CURPrefix)
	if err != nil {
		return nil, err
	}

	// Every failure below this point — an unparseable manifest, a report
	// part in a format this ingestor cannot read, one part's transient S3
	// error — is deliberately non-fatal to the call as a whole, the same
	// per-item-failure-is-a-warning-not-an-abort discipline
	// internal/adapters/aws/discovery uses: ports.CostIngestor.Fetch's
	// signature carries no Warnings slice to report them through, so
	// they are dropped rather than escalated, but a caller can always
	// tell an empty, error-free result apart from a systemic failure
	// (bucket unreachable, credentials revoked), which does still return
	// an error above, at the bucketRegion/listManifests calls.
	var records []cost.Record
	for _, key := range manifestKeys {
		m, err := c.fetchManifest(ctx, regional, in.Account.CURBucket, key)
		if err != nil {
			continue
		}
		if !periodsOverlap(m.billingStart, m.billingEnd, in.Period.Start, in.Period.End) {
			continue
		}
		for _, partKey := range m.reportKeys {
			if strings.HasSuffix(strings.ToLower(partKey), ".parquet") {
				// No Parquet reader is available among this codebase's
				// allowed dependencies (stdlib only, plus the pinned AWS
				// SDK modules) — a Data Export configured for Parquet
				// output cannot be parsed here. This is a known,
				// documented gap: the customer needs to configure their
				// report/export for CSV output for this ingestor to see
				// its data at all.
				continue
			}
			partRecords, err := c.streamReportPart(ctx, regional, in, in.Account.CURBucket, partKey)
			if err != nil {
				continue
			}
			records = append(records, partRecords...)
		}
	}
	return records, nil
}

func (c *CURIngestor) bucketRegion(ctx context.Context, client curS3API, bucket string) (string, error) {
	resp, err := client.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String(bucket)})
	if err != nil {
		return "", err
	}
	region := string(resp.LocationConstraint)
	if region == "" {
		region = "us-east-1" // AWS returns an empty constraint for us-east-1 specifically
	}
	return region, nil
}

// listManifests walks every object under bucket/prefix and returns the keys
// ending in "Manifest.json" — both the legacy "<report>-Manifest.json" name
// and the Data Exports "manifest.json" name match this suffix check.
func (c *CURIngestor) listManifests(ctx context.Context, client curS3API, bucket, prefix string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, awserr.Translate(err, "s3", "ListObjectsV2", "s3:ListObjectsV2")
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if strings.HasSuffix(key, "Manifest.json") {
				keys = append(keys, key)
			}
		}
	}
	return keys, nil
}

func (c *CURIngestor) fetchManifest(ctx context.Context, client curS3API, bucket, key string) (*curManifest, error) {
	obj, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, awserr.Translate(err, "s3", "GetObject", "s3:GetObject")
	}
	defer obj.Body.Close()
	body, err := io.ReadAll(obj.Body)
	if err != nil {
		return nil, err
	}
	return parseManifest(body)
}

// streamReportPart downloads one gzip-compressed CUR CSV part and parses it
// row by row without buffering the decompressed file in memory: GetObject's
// body feeds a gzip.Reader which feeds a csv.Reader, and each row becomes
// (at most) one cost.Record before the next row is read.
func (c *CURIngestor) streamReportPart(ctx context.Context, client curS3API, in ports.CostIngestInput, bucket, key string) ([]cost.Record, error) {
	obj, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, awserr.Translate(err, "s3", "GetObject", "s3:GetObject")
	}
	defer obj.Body.Close()

	var reader io.Reader = obj.Body
	if strings.HasSuffix(strings.ToLower(key), ".gz") {
		gz, err := gzip.NewReader(obj.Body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}

	csvReader := csv.NewReader(reader)
	csvReader.FieldsPerRecord = -1 // CUR schemas vary row to row only in trailing optional columns; do not hard-fail on that

	header, err := csvReader.Read()
	if err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, err
	}
	cols := curColumnIndex(header)

	basis := in.Basis
	if basis == "" {
		basis = cost.BasisAmortized
	}

	var records []cost.Record
	for {
		row, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return records, err // return what was parsed so far alongside the error
		}
		rec, ok := curRecordFromRow(in, header, cols, row, basis)
		if ok {
			records = append(records, rec)
		}
	}
	return records, nil
}

// curManifest is the normalized shape both manifest layouts reduce to.
type curManifest struct {
	billingStart, billingEnd time.Time
	reportKeys               []string
}

// rawCURManifest models the well-established legacy CUR manifest schema
// (assemblyId/billingPeriod/reportKeys — unchanged since CUR's original
// release and used by every CUR-parsing tool in the ecosystem) plus, on a
// best-effort basis, the newer Data Exports manifest's "dataFiles" array,
// whose exact field names are less publicly documented; a manifest that
// matches neither known shape returns an error from parseManifest rather
// than silently producing zero report keys.
type rawCURManifest struct {
	AssemblyID    string   `json:"assemblyId"`
	ReportKeys    []string `json:"reportKeys"`
	BillingPeriod *struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"billingPeriod"`
	DataFiles []struct {
		Key string `json:"key"`
	} `json:"dataFiles"`
}

func parseManifest(body []byte) (*curManifest, error) {
	var raw rawCURManifest
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	m := &curManifest{}
	if raw.BillingPeriod != nil {
		if t, err := parseCURTimestamp(raw.BillingPeriod.Start); err == nil {
			m.billingStart = t
		}
		if t, err := parseCURTimestamp(raw.BillingPeriod.End); err == nil {
			m.billingEnd = t
		}
	}
	if len(raw.ReportKeys) > 0 {
		m.reportKeys = raw.ReportKeys
	} else {
		for _, f := range raw.DataFiles {
			if f.Key != "" {
				m.reportKeys = append(m.reportKeys, f.Key)
			}
		}
	}
	if len(m.reportKeys) == 0 {
		return nil, core.Invalid("costing: manifest has no recognizable report file list (neither reportKeys nor dataFiles)")
	}
	return m, nil
}

// parseCURTimestamp accepts both timestamp shapes CUR manifests use across
// versions: "20260801T000000.000Z" (legacy, no separators) and RFC3339.
func parseCURTimestamp(s string) (time.Time, error) {
	if t, err := time.Parse("20060102T150405.000Z", s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

func periodsOverlap(aStart, aEnd, bStart, bEnd time.Time) bool {
	if aStart.IsZero() || aEnd.IsZero() {
		// A manifest whose billing period could not be parsed is included
		// rather than silently dropped — better to read a report that
		// turns out to be outside the window than to miss one that was
		// inside it because of a timestamp format this parser doesn't
		// recognize.
		return true
	}
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}

// curColumnIndex maps a CUR header row's column names ("lineItem/UsageType",
// "resourceTags/user:Environment", ...) to their position, so row parsing
// never depends on column order — CUR's column set varies by report
// configuration (whether resource IDs, RI/SP amortization details, resource
// tags, etc. are included).
func curColumnIndex(header []string) map[string]int {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[h] = i
	}
	return idx
}

func curCol(row []string, idx map[string]int, name string) string {
	i, ok := idx[name]
	if !ok || i >= len(row) {
		return ""
	}
	return row[i]
}

// curRecordFromRow maps one CUR CSV row to a cost.Record. Tax, credit,
// refund and fee lines are kept (with their own ChargeType) rather than
// filtered out here — cost.ChargeType.Attributable() is what the cost
// engine already uses downstream to decide whether a line participates in
// workload attribution, and this ingestor's job is to report what AWS
// billed, not to pre-judge which lines matter.
func curRecordFromRow(in ports.CostIngestInput, header []string, idx map[string]int, row []string, basis cost.AmortizationBasis) (cost.Record, bool) {
	amountStr := curAmortizedAmount(idx, row, basis)
	amount, err := strconv.ParseFloat(amountStr, 64)
	if err != nil || amount == 0 {
		return cost.Record{}, false
	}

	start, _ := time.Parse(time.RFC3339, curCol(row, idx, "lineItem/UsageStartDate"))
	end, _ := time.Parse(time.RFC3339, curCol(row, idx, "lineItem/UsageEndDate"))
	period := in.Period
	if !start.IsZero() && !end.IsZero() {
		period = core.NewPeriod(start, end)
	}

	service := curCol(row, idx, "product/ProductName")
	if service == "" {
		service = curCol(row, idx, "lineItem/ProductCode")
	}

	tags := curResourceTags(header, row)
	env := core.EnvUnknown
	if v, ok := tags.Get("Environment"); ok {
		env = core.NormalizeEnvironment(v)
	}

	usageQty, _ := strconv.ParseFloat(curCol(row, idx, "lineItem/UsageAmount"), 64)

	accountID := curCol(row, idx, "lineItem/UsageAccountId")
	if accountID == "" {
		accountID = string(in.Account.AccountID)
	}

	return cost.Record{
		ID: core.NewID("cost"), TenantID: in.TenantID, AccountID: core.AccountID(accountID),
		Region: core.Region(curCol(row, idx, "product/region")), AZ: curCol(row, idx, "lineItem/AvailabilityZone"),
		Period: period, Granularity: cost.GranularityHourly,
		Service: service, UsageType: curCol(row, idx, "lineItem/UsageType"), Operation: curCol(row, idx, "lineItem/Operation"),
		ResourceARN: core.ARN(curCol(row, idx, "lineItem/ResourceId")),
		ChargeType:  curLineItemChargeType(curCol(row, idx, "lineItem/LineItemType")),
		Basis:       basis, Amount: core.USDollars(amount),
		UsageQty: usageQty, UsageUnit: curCol(row, idx, "pricing/unit"),
		Tags: tags, Environment: env, Source: "cur", IngestedAt: time.Now().UTC(),
	}, true
}

// curAmortizedAmount picks the CUR column that answers "how much did this
// line cost" for the requested basis, following the standard CUR
// amortization recipe: a reservation- or savings-plan-covered usage line's
// unblended cost is its discounted sticker price, not what actually funded
// it — reservation/EffectiveCost and savingsPlan/SavingsPlanEffectiveCost
// spread that commitment's upfront/recurring fee back across the usage it
// covered, which is the whole point of reporting amortized cost as the
// primary basis (see cost.AmortizationBasis's doc comment). Any line type
// without a more specific effective-cost column — On-Demand usage, taxes,
// fees — falls back to UnblendedCost, which for those line types already
// equals what amortization would report.
func curAmortizedAmount(idx map[string]int, row []string, basis cost.AmortizationBasis) string {
	lineItemType := curCol(row, idx, "lineItem/LineItemType")
	switch basis {
	case cost.BasisUnblended:
		return curCol(row, idx, "lineItem/UnblendedCost")
	case cost.BasisBlended:
		if v := curCol(row, idx, "lineItem/BlendedCost"); v != "" {
			return v
		}
		return curCol(row, idx, "lineItem/UnblendedCost")
	default: // BasisAmortized, BasisNetAmortized
		switch lineItemType {
		case "DiscountedUsage":
			if v := curCol(row, idx, "reservation/EffectiveCost"); v != "" {
				return v
			}
		case "SavingsPlanCoveredUsage":
			if v := curCol(row, idx, "savingsPlan/SavingsPlanEffectiveCost"); v != "" {
				return v
			}
		}
		return curCol(row, idx, "lineItem/UnblendedCost")
	}
}

func curLineItemChargeType(raw string) cost.ChargeType {
	switch raw {
	case "Tax":
		return cost.ChargeTax
	case "Credit":
		return cost.ChargeCredit
	case "Refund":
		return cost.ChargeRefund
	case "Fee", "SavingsPlanUpfrontFee":
		return cost.ChargeFee
	case "RIFee":
		return cost.ChargeRIFee
	case "SavingsPlanRecurringFee":
		return cost.ChargeSavingsPlanRecurring
	case "SavingsPlanNegation":
		return cost.ChargeDiscount
	default: // "Usage", "DiscountedUsage", "SavingsPlanCoveredUsage"
		return cost.ChargeUsage
	}
}

// curResourceTags extracts every "resourceTags/user:*" and
// "resourceTags/aws:*" column into core.Tags, stripping the CUR-specific
// prefix so a tag key here matches the same tag key a discovery adapter
// would report for the same resource.
func curResourceTags(header, row []string) core.Tags {
	tags := core.Tags{}
	for i, name := range header {
		if i >= len(row) || row[i] == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(name, "resourceTags/user:"); ok {
			tags[rest] = row[i]
		} else if rest, ok := strings.CutPrefix(name, "resourceTags/aws:"); ok {
			tags[rest] = row[i]
		}
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}
