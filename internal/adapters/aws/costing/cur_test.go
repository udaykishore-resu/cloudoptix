package costing

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/cost"
	"github.com/udaykishore-resu/cloudoptix/internal/ports"
)

// curTestPeriod covers the same August 2026 billing period as
// curLegacyManifest so tests default to a period the fixture manifest
// overlaps.
func curTestPeriod() core.Period {
	return core.NewPeriod(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
}

// gzipCSV builds a gzip-compressed CSV body from a header and rows, exactly
// the shape a real CUR report part takes in S3.
func gzipCSV(t *testing.T, header []string, rows [][]string) []byte {
	t.Helper()
	var csvBuf bytes.Buffer
	w := csv.NewWriter(&csvBuf)
	require.NoError(t, w.Write(header))
	for _, r := range rows {
		require.NoError(t, w.Write(r))
	}
	w.Flush()
	require.NoError(t, w.Error())

	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	_, err := gz.Write(csvBuf.Bytes())
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	return gzBuf.Bytes()
}

type fakeCURObject struct {
	key  string
	body []byte
}

type fakeCURS3 struct {
	objects []fakeCURObject
}

func (f *fakeCURS3) HeadBucket(context.Context, *s3.HeadBucketInput, ...func(*s3.Options)) (*s3.HeadBucketOutput, error) {
	return &s3.HeadBucketOutput{}, nil
}
func (f *fakeCURS3) GetBucketLocation(context.Context, *s3.GetBucketLocationInput, ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error) {
	return &s3.GetBucketLocationOutput{LocationConstraint: s3types.BucketLocationConstraint("us-east-1")}, nil
}
func (f *fakeCURS3) ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	var contents []s3types.Object
	for _, o := range f.objects {
		contents = append(contents, s3types.Object{Key: aws.String(o.key)})
	}
	return &s3.ListObjectsV2Output{Contents: contents}, nil
}
func (f *fakeCURS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	key := aws.ToString(in.Key)
	for _, o := range f.objects {
		if o.key == key {
			return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(o.body))}, nil
		}
	}
	return nil, &notFoundErr{key: key}
}

type notFoundErr struct{ key string }

func (e *notFoundErr) Error() string { return "no such key: " + e.key }

func curTestIngestor(f *fakeCURS3) *CURIngestor {
	return &CURIngestor{
		newClient:      func(aws.Config) curS3API { return f },
		regionalClient: func(aws.Config, string) curS3API { return f },
	}
}

const curLegacyManifest = `{
  "assemblyId": "abc-123",
  "account": "222222222222",
  "reportName": "cloudoptix-cur",
  "billingPeriod": {"start": "20260801T000000.000Z", "end": "20260901T000000.000Z"},
  "bucket": "cur-bucket",
  "reportKeys": ["cur/cloudoptix-cur/20260801-20260901/cloudoptix-cur-00001.csv.gz"]
}`

var curHeader = []string{
	"identity/LineItemId", "bill/BillingPeriodStartDate", "bill/BillingPeriodEndDate",
	"lineItem/UsageAccountId", "lineItem/LineItemType", "lineItem/UsageStartDate", "lineItem/UsageEndDate",
	"lineItem/ProductCode", "product/ProductName", "lineItem/UsageType", "lineItem/Operation",
	"lineItem/AvailabilityZone", "product/region", "lineItem/ResourceId", "lineItem/UsageAmount",
	"lineItem/UnblendedCost", "pricing/unit", "reservation/EffectiveCost", "savingsPlan/SavingsPlanEffectiveCost",
	"resourceTags/user:Environment",
}

func TestCURIngestor_ParsesUsageLineWithResourceAttribution(t *testing.T) {
	rows := [][]string{
		{
			"li-1", "2026-08-01T00:00:00Z", "2026-09-01T00:00:00Z",
			"222222222222", "Usage", "2026-08-15T10:00:00Z", "2026-08-15T11:00:00Z",
			"AmazonEC2", "Amazon Elastic Compute Cloud - Compute", "BoxUsage:m5.large", "RunInstances",
			"us-east-1a", "us-east-1", "arn:aws:ec2:us-east-1:222222222222:instance/i-0abc123", "1",
			"0.096", "Hrs", "", "", "prod",
		},
	}
	gz := gzipCSV(t, curHeader, rows)
	f := &fakeCURS3{objects: []fakeCURObject{
		{key: "cur/cloudoptix-cur/cloudoptix-cur-Manifest.json", body: []byte(curLegacyManifest)},
		{key: "cur/cloudoptix-cur/20260801-20260901/cloudoptix-cur-00001.csv.gz", body: gz},
	}}
	ing := curTestIngestor(f)

	out, err := ing.Fetch(context.Background(), ports.CostIngestInput{
		TenantID: "tenant-1", Session: testSession(), Account: testAccountWithCUR(),
		Period:      curTestPeriod(),
		Granularity: cost.GranularityHourly, Basis: cost.BasisAmortized, ResourceLevel: true,
	})
	require.NoError(t, err)
	require.Len(t, out, 1)

	rec := out[0]
	assert.Equal(t, "Amazon Elastic Compute Cloud - Compute", rec.Service)
	assert.Equal(t, "BoxUsage:m5.large", rec.UsageType)
	assert.Equal(t, "arn:aws:ec2:us-east-1:222222222222:instance/i-0abc123", string(rec.ResourceARN))
	assert.Equal(t, cost.ChargeUsage, rec.ChargeType)
	assert.InDelta(t, 0.096, rec.Amount.Units(), 0.0001)
	assert.Equal(t, "prod", rec.Tags["Environment"])
	assert.Equal(t, "cur", rec.Source)
}

func TestCURIngestor_PrefersEffectiveCostForDiscountedUsageWhenAmortized(t *testing.T) {
	rows := [][]string{
		{
			"li-2", "2026-08-01T00:00:00Z", "2026-09-01T00:00:00Z",
			"222222222222", "DiscountedUsage", "2026-08-15T10:00:00Z", "2026-08-15T11:00:00Z",
			"AmazonEC2", "Amazon Elastic Compute Cloud - Compute", "BoxUsage:m5.large", "RunInstances",
			"us-east-1a", "us-east-1", "arn:aws:ec2:us-east-1:222222222222:instance/i-0reserved", "1",
			"0.096", "Hrs", "0.061", "", "",
		},
	}
	gz := gzipCSV(t, curHeader, rows)
	f := &fakeCURS3{objects: []fakeCURObject{
		{key: "cur/cloudoptix-cur/cloudoptix-cur-Manifest.json", body: []byte(curLegacyManifest)},
		{key: "cur/cloudoptix-cur/20260801-20260901/cloudoptix-cur-00001.csv.gz", body: gz},
	}}
	ing := curTestIngestor(f)

	out, err := ing.Fetch(context.Background(), ports.CostIngestInput{
		TenantID: "tenant-1", Session: testSession(), Account: testAccountWithCUR(),
		Period: curTestPeriod(), Basis: cost.BasisAmortized,
	})
	require.NoError(t, err)
	require.Len(t, out, 1)
	// Amortized basis must prefer reservation/EffectiveCost (0.061) over the
	// sticker-price lineItem/UnblendedCost (0.096) for a reservation-covered line.
	assert.InDelta(t, 0.061, out[0].Amount.Units(), 0.0001)
}

func TestCURIngestor_ManifestOutsidePeriodIsSkipped(t *testing.T) {
	rows := [][]string{{
		"li-3", "2026-08-01T00:00:00Z", "2026-09-01T00:00:00Z", "222222222222", "Usage",
		"2026-08-15T10:00:00Z", "2026-08-15T11:00:00Z", "AmazonEC2", "Amazon EC2", "BoxUsage", "RunInstances",
		"us-east-1a", "us-east-1", "i-x", "1", "5.00", "Hrs", "", "", "",
	}}
	gz := gzipCSV(t, curHeader, rows)
	f := &fakeCURS3{objects: []fakeCURObject{
		{key: "cur/cloudoptix-cur/cloudoptix-cur-Manifest.json", body: []byte(curLegacyManifest)},
		{key: "cur/cloudoptix-cur/20260801-20260901/cloudoptix-cur-00001.csv.gz", body: gz},
	}}
	ing := curTestIngestor(f)

	// Request a period entirely before the manifest's billing period.
	out, err := ing.Fetch(context.Background(), ports.CostIngestInput{
		TenantID: "tenant-1", Session: testSession(), Account: testAccountWithCUR(),
		Period: core.NewPeriod(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)),
	})
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestCURIngestor_Available(t *testing.T) {
	f := &fakeCURS3{}
	ing := curTestIngestor(f)
	assert.True(t, ing.Available(context.Background(), testSession(), testAccountWithCUR()))

	noBucket := testAccount()
	assert.False(t, ing.Available(context.Background(), testSession(), noBucket))
}

func TestCURIngestor_Source(t *testing.T) {
	assert.Equal(t, "cur", NewCURIngestor().Source())
}

func TestParseManifest_LegacyShape(t *testing.T) {
	m, err := parseManifest([]byte(curLegacyManifest))
	require.NoError(t, err)
	require.Len(t, m.reportKeys, 1)
	assert.Equal(t, "cur/cloudoptix-cur/20260801-20260901/cloudoptix-cur-00001.csv.gz", m.reportKeys[0])
	assert.Equal(t, 2026, m.billingStart.Year())
}

func TestParseManifest_DataExportsShape(t *testing.T) {
	body := `{"id":"exp-1","dataFiles":[{"key":"exports/cur2/data/BILLING_PERIOD=2026-08/part-0.csv.gz"}]}`
	m, err := parseManifest([]byte(body))
	require.NoError(t, err)
	require.Len(t, m.reportKeys, 1)
	assert.Equal(t, "exports/cur2/data/BILLING_PERIOD=2026-08/part-0.csv.gz", m.reportKeys[0])
}

func TestParseManifest_UnrecognizedShapeErrors(t *testing.T) {
	_, err := parseManifest([]byte(`{"somethingElse": true}`))
	assert.Error(t, err)
}

func TestCURIngestor_ParquetPartIsSkippedWithWarning(t *testing.T) {
	manifest := strings.Replace(curLegacyManifest, ".csv.gz", ".parquet", 1)
	f := &fakeCURS3{objects: []fakeCURObject{
		{key: "cur/cloudoptix-cur/cloudoptix-cur-Manifest.json", body: []byte(manifest)},
	}}
	ing := curTestIngestor(f)
	out, err := ing.Fetch(context.Background(), ports.CostIngestInput{
		TenantID: "tenant-1", Session: testSession(), Account: testAccountWithCUR(), Period: curTestPeriod(),
	})
	require.NoError(t, err)
	assert.Empty(t, out)
}
