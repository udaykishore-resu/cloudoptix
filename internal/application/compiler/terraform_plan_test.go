package compiler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/simulate"
)

const samplePlanJSON = `{
  "format_version": "1.2",
  "resource_changes": [
    {
      "address": "aws_instance.api",
      "type": "aws_instance",
      "name": "api",
      "change": {
        "actions": ["create"],
        "before": null,
        "after": {"instance_type": "m5.large", "availability_zone": "us-east-1a", "tags": {"Owner": "platform"}}
      }
    },
    {
      "address": "aws_instance.legacy",
      "type": "aws_instance",
      "name": "legacy",
      "change": {
        "actions": ["delete", "create"],
        "before": {"instance_type": "m5.large", "availability_zone": "us-east-1a"},
        "after": {"instance_type": "m5.xlarge", "availability_zone": "us-east-1a"}
      }
    },
    {
      "address": "aws_instance.worker[0]",
      "type": "aws_instance",
      "name": "worker",
      "index": 0,
      "change": {
        "actions": ["create"],
        "before": null,
        "after": {"instance_type": "m5.large", "availability_zone": "us-east-1b"}
      }
    },
    {
      "address": "aws_instance.worker[1]",
      "type": "aws_instance",
      "name": "worker",
      "index": 1,
      "change": {
        "actions": ["create"],
        "before": null,
        "after": {"instance_type": "m5.large", "availability_zone": "us-east-1b"}
      }
    },
    {
      "address": "aws_security_group.default",
      "type": "aws_security_group",
      "name": "default",
      "change": {
        "actions": ["no-op"],
        "before": {},
        "after": {}
      }
    },
    {
      "address": "data.aws_ami.al2",
      "type": "aws_ami",
      "name": "al2",
      "change": {
        "actions": ["read"],
        "before": null,
        "after": null
      }
    }
  ]
}`

func TestParseTerraformPlanJSON_ActionsAndCountExpansion(t *testing.T) {
	raws, warnings, err := ParseTerraformPlanJSON([]byte(samplePlanJSON), "us-west-2")
	require.NoError(t, err)
	assert.Empty(t, warnings)

	// no-op and read (data source) entries are dropped entirely.
	require.Len(t, raws, 4)

	byAddr := map[string]RawResource{}
	for _, r := range raws {
		byAddr[r.Address] = r
	}

	create := byAddr["aws_instance.api"]
	assert.Equal(t, simulate.ChangeCreate, create.Action)
	assert.Nil(t, create.Before)
	assert.Equal(t, "m5.large", create.After.Str("instance_type", ""))
	assert.Equal(t, "us-east-1", string(create.Region), "region recovered from availability_zone")
	assert.Equal(t, map[string]string{"Owner": "platform"}, create.Tags)

	replaced := byAddr["aws_instance.legacy"]
	assert.Equal(t, simulate.ChangeReplace, replaced.Action, "a two-action list is always a replace")
	assert.Equal(t, "m5.large", replaced.Before.Str("instance_type", ""))
	assert.Equal(t, "m5.xlarge", replaced.After.Str("instance_type", ""))

	// count expansion: two distinct resource_changes entries for the same
	// base address, exactly what terraform show -json already produces.
	w0, ok0 := byAddr["aws_instance.worker[0]"]
	w1, ok1 := byAddr["aws_instance.worker[1]"]
	require.True(t, ok0)
	require.True(t, ok1)
	assert.Equal(t, "aws_instance.worker", BaseAddress(w0.Address))
	assert.Equal(t, "aws_instance.worker", BaseAddress(w1.Address))
}

func TestParseTerraformPlanJSON_RejectsNonPlanInput(t *testing.T) {
	_, _, err := ParseTerraformPlanJSON([]byte(`{"foo": "bar"}`), "us-east-1")
	assert.Error(t, err)
}

func TestParseTerraformPlanJSON_InvalidJSON(t *testing.T) {
	_, _, err := ParseTerraformPlanJSON([]byte(`not json`), "us-east-1")
	assert.Error(t, err)
}
