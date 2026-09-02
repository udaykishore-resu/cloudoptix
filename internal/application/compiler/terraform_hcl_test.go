package compiler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

const sampleHCL = `
resource "aws_instance" "web" {
  instance_type = "m5.large"
  availability_zone = "us-east-1a"

  root_block_device {
    volume_size = 100
    volume_type = "gp3"
  }

  tags = {
    Owner = "platform"
  }
}

resource "aws_autoscaling_group" "workers" {
  desired_capacity = 3
  count             = 2
  min_size          = 1
  max_size          = 5
}

resource "aws_s3_bucket" "assets" {
  bucket = "my-assets-${var.env}"
}
`

func TestParseTerraformHCL_TopLevelAttributes(t *testing.T) {
	raws, _, err := ParseTerraformHCL([]byte(sampleHCL), "us-west-2")
	require.NoError(t, err)
	require.Len(t, raws, 3)

	byType := map[string]RawResource{}
	for _, r := range raws {
		byType[r.Type] = r
	}

	web := byType["aws_instance"]
	assert.Equal(t, simulate.ChangeCreate, web.Action, "raw HCL has no diff concept; every block is a create")
	assert.Nil(t, web.Before)
	assert.Equal(t, "m5.large", web.After.Str("instance_type", ""))
	// root_block_device is a nested block; the scanner does not descend into it.
	assert.Nil(t, web.After.FirstMap("root_block_device"))
	// tags is a map literal, not a scalar; the scanner deliberately does not
	// parse list/map attribute values (see ParseTerraformHCL's doc comment).
	assert.Empty(t, web.Tags)

	asg := byType["aws_autoscaling_group"]
	assert.Equal(t, 3.0, asg.After.Float("desired_capacity", 0))
	assert.NotEmpty(t, asg.Warnings, "count meta-argument must be flagged")

	bucket := byType["aws_s3_bucket"]
	// "my-assets-${var.env}" contains interpolation and is skipped, not guessed.
	assert.Equal(t, "", bucket.After.Str("bucket", ""))
}

func TestParseTerraformHCL_NestedBlocksDoNotConfuseBraceDepth(t *testing.T) {
	src := `
resource "aws_instance" "web" {
  instance_type = "m5.large"
  root_block_device {
    volume_size = 100
  }
  ebs_block_device {
    volume_size = 200
  }
  instance_market_options {
    market_type = "spot"
  }
}
resource "aws_s3_bucket" "logs" {
  bucket = "logs"
}
`
	raws, _, err := ParseTerraformHCL([]byte(src), "us-east-1")
	require.NoError(t, err)
	require.Len(t, raws, 2, "the resource block boundary must close correctly despite three nested blocks")
	assert.Equal(t, "aws_instance", raws[0].Type)
	assert.Equal(t, "aws_s3_bucket", raws[1].Type)
	assert.Equal(t, "logs", raws[1].After.Str("bucket", ""))
}
