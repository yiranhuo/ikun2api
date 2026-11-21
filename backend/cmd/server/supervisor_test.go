package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
)

// A staged upgrade must be recognised from the journal alone. The supervisor
// uses this helper to tell "restart into the candidate" apart from "journal
// written but the atomic swap never landed", which has to be left alone so the
// startup path can discard it as an interrupted upgrade.
func TestUpgradeCandidateInPlaceDetectsTheStagedExecutable(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "ikun2api")
	require.NoError(t, os.WriteFile(binary, []byte("candidate"), 0o755))
	candidateHash, err := service.UpgradeFileHash(binary)
	require.NoError(t, err)

	journal := &service.UpgradeJournal{
		BackupDirectory: filepath.Join(dir, "upgrade-backups", "staged"),
		PreviousBinary:  binary + ".backup",
		PreviousSHA256:  strings.Repeat("a", 64),
		CandidateSHA256: candidateHash,
		CreatedAt:       time.Now().UTC(),
	}

	require.True(t, upgradeCandidateInPlace(binary, journal))

	// Before the swap the live executable still hashes to the previous release.
	require.NoError(t, os.WriteFile(binary, []byte("previous"), 0o755))
	require.False(t, upgradeCandidateInPlace(binary, journal))

	require.False(t, upgradeCandidateInPlace(filepath.Join(dir, "absent"), journal))
}
