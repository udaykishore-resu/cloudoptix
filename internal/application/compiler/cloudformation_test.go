package compiler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sampleCFNJSON = `{
  "Resources": {
    "WebServer": {
      "Type": "AWS::EC2::Instance",
      "Properties": {
        "InstanceType": "m5.large",
        "Tags": [{"Key": "Owner", "Value": "platform"}]
      }
    },
    "AppQueue": {
      "Type": "AWS::SQS::Queue",
      "Properties": {}
    }
  }
}`

func TestParseCloudFormationJSON(t *testing.T) {
	raws, _, err := ParseCloudFormationJSON([]byte(sampleCFNJSON), "us-east-1")
	require.NoError(t, err)
	require.Len(t, raws, 2)

	byType := map[string]RawResource{}
	for _, r := range raws {
		byType[r.Type] = r
	}
	inst := byType["aws_instance"]
	assert.Equal(t, "m5.large", inst.After.Str("instance_type", ""))
	assert.Equal(t, map[string]string{"Owner": "platform"}, inst.Tags)
	assert.Contains(t, inst.Address, "WebServer")

	assert.Contains(t, byType, "aws_sqs_queue")
}

const sampleCFNYAML = `
Resources:
  Cache:
    Type: AWS::ElastiCache::CacheCluster
    Properties:
      CacheNodeType: cache.r6g.large
      Engine: redis
      NumCacheNodes: 2
  Bucket:
    Type: AWS::S3::Bucket
    Properties: {}
  Mystery:
    Type: AWS::Custom::Widget
    Properties: {}
`

func TestParseCloudFormationYAML(t *testing.T) {
	raws, _, err := ParseCloudFormationYAML([]byte(sampleCFNYAML), "us-east-1")
	require.NoError(t, err)
	require.Len(t, raws, 3)

	byType := map[string]RawResource{}
	for _, r := range raws {
		byType[r.Type] = r
	}
	cache := byType["aws_elasticache_cluster"]
	assert.Equal(t, "cache.r6g.large", cache.After.Str("node_type", ""))
	assert.Equal(t, 2.0, cache.After.Float("num_cache_nodes", 0))

	// An unrecognised CFN type falls through with its own type name so it can
	// still be reported (as Unpriced) rather than vanishing.
	assert.Equal(t, "AWS::Custom::Widget", byType["AWS::Custom::Widget"].Type)
}

func TestParseCloudFormationJSON_RejectsEmptyTemplate(t *testing.T) {
	_, _, err := ParseCloudFormationJSON([]byte(`{"Resources": {}}`), "us-east-1")
	assert.Error(t, err)
}
