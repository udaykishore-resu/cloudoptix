package memstore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/udaykishore-resu/cloudoptix/internal/domain/audit"
	"github.com/udaykishore-resu/cloudoptix/internal/domain/core"
)

func TestAuditRepo_ChainAppendAndVerify(t *testing.T) {
	s := New()
	repo := s.Repositories().Audit
	ctx := ctxFor(tenantA)

	var lastHash string
	for i := 0; i < 5; i++ {
		rec := audit.Record{
			TenantID: tenantA,
			Action:   audit.ActionExecutionSucceeded,
			Outcome:  audit.OutcomeSuccess,
			Actor:    "worker-1",
			Message:  "step ok",
		}
		sealed, err := repo.Append(ctx, rec)
		require.NoError(t, err)
		assert.Equal(t, int64(i+1), sealed.Sequence)
		assert.Equal(t, lastHash, sealed.PrevHash)
		assert.NotEmpty(t, sealed.Hash)
		lastHash = sealed.Hash
	}

	head, seq, err := repo.Head(ctx, tenantA)
	require.NoError(t, err)
	assert.Equal(t, lastHash, head)
	assert.Equal(t, int64(5), seq)

	v, err := repo.Verify(ctx, tenantA, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.True(t, v.Valid)
	assert.Equal(t, 5, v.RecordsChecked)
	assert.Nil(t, v.FirstBreakAt)
}

func TestAuditRepo_VerifyDetectsTamper(t *testing.T) {
	s := New()
	repo := s.Repositories().Audit
	ctx := ctxFor(tenantA)

	for i := 0; i < 4; i++ {
		_, err := repo.Append(ctx, audit.Record{
			TenantID: tenantA,
			Action:   audit.ActionExecutionSucceeded,
			Outcome:  audit.OutcomeSuccess,
			Actor:    "worker-1",
			Message:  "step ok",
		})
		require.NoError(t, err)
	}

	// Reach directly into the store (white-box: same package) to simulate
	// tampering with a persisted record's content without going through
	// Append — exactly the class of change the hash chain exists to detect.
	s.auditMu.Lock()
	records := s.data.AuditRecords[tenantA]
	require.Len(t, records, 4)
	records[2].Message = "not what actually happened"
	s.data.AuditRecords[tenantA] = records
	s.auditMu.Unlock()

	v, err := repo.Verify(ctx, tenantA, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.False(t, v.Valid)
	require.NotNil(t, v.FirstBreakAt)
	assert.Equal(t, int64(3), *v.FirstBreakAt) // sequence numbers are 1-based
	assert.NotEmpty(t, v.BreakDetail)
}

func TestAuditRepo_VerifyDetectsBrokenChainLink(t *testing.T) {
	s := New()
	repo := s.Repositories().Audit
	ctx := ctxFor(tenantA)

	for i := 0; i < 3; i++ {
		_, err := repo.Append(ctx, audit.Record{TenantID: tenantA, Action: audit.ActionExecutionSucceeded, Outcome: audit.OutcomeSuccess, Actor: "worker-1"})
		require.NoError(t, err)
	}

	s.auditMu.Lock()
	records := s.data.AuditRecords[tenantA]
	records[1].PrevHash = "forged-hash-that-does-not-match-record-0"
	s.data.AuditRecords[tenantA] = records
	s.auditMu.Unlock()

	v, err := repo.Verify(ctx, tenantA, time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.False(t, v.Valid)
	require.NotNil(t, v.FirstBreakAt)
	assert.Equal(t, int64(2), *v.FirstBreakAt)
}

func TestAuditRepo_ChainIsPerTenant(t *testing.T) {
	s := New()
	repo := s.Repositories().Audit

	a1, err := repo.Append(ctxFor(tenantA), audit.Record{TenantID: tenantA, Action: audit.ActionLogin, Outcome: audit.OutcomeSuccess, Actor: "alice"})
	require.NoError(t, err)
	b1, err := repo.Append(ctxFor(tenantB), audit.Record{TenantID: tenantB, Action: audit.ActionLogin, Outcome: audit.OutcomeSuccess, Actor: "bob"})
	require.NoError(t, err)

	// Both are the first record of their own tenant's chain: sequence 1, no
	// predecessor, regardless of interleaving.
	assert.Equal(t, int64(1), a1.Sequence)
	assert.Equal(t, int64(1), b1.Sequence)
	assert.Empty(t, a1.PrevHash)
	assert.Empty(t, b1.PrevHash)
	assert.NotEqual(t, a1.Hash, b1.Hash)
}

func TestAuditRepo_TenantIsolation(t *testing.T) {
	s := New()
	repo := s.Repositories().Audit

	_, err := repo.Append(ctxFor(tenantA), audit.Record{TenantID: tenantA, Action: audit.ActionLogin, Outcome: audit.OutcomeSuccess, Actor: "alice"})
	require.NoError(t, err)

	_, err = repo.Verify(ctxFor(tenantB), tenantA, time.Time{}, time.Time{})
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrTenantMismatch)
}
