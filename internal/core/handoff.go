package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PeerWorkerEnv opts a forked peer process into run-id handoff. Plain
// `go test` is single-process and does not need it.
const PeerWorkerEnv = "TRACERA_PEER_WORKER"

// HandoffKeyEnv is an optional shared launch id for peer handoff. Peers that
// must report into one run set the same value (the parent process / CI job
// generates it). When unset, well-known CI job ids are used when present so
// sequential jobs with the same TRACERA_RUN_TITLE do not collide; otherwise
// the key is project id + run title only.
const HandoffKeyEnv = "TRACERA_HANDOFF_KEY"

// PeerCountEnv is the expected number of peer processes for one launch.
// When set, ReleasePeerRun does not finish while claimed < N even if refs
// hit 0 — a late peer can still attach. Without it, finish on refs==0
// (peers that never overlap still risk N× create_run — set the count).
const PeerCountEnv = "TRACERA_PEER_COUNT"

// peerWorkerLabel renders a worker id — from the env opt-in or from a runner
// that detected the fork itself — as a log label.
func peerWorkerLabel(worker string) string {
	if worker = strings.TrimSpace(worker); worker != "" {
		return fmt.Sprintf("go peer worker (%s)", worker)
	}
	return ""
}

// PeerWorkerLabel returns a short label when this process is an opted-in peer
// worker, else "".
func PeerWorkerLabel(env map[string]string) string {
	if env == nil {
		env = EnvMap()
	}
	return peerWorkerLabel(env[PeerWorkerEnv])
}

// handoffLaunchKey is the extra scope that keeps one suite launch apart from
// the next. Explicit TRACERA_HANDOFF_KEY wins; else a CI job id; else "".
func handoffLaunchKey(env map[string]string) string {
	if env == nil {
		env = EnvMap()
	}
	if key := strings.TrimSpace(env[HandoffKeyEnv]); key != "" {
		return key
	}
	return ciLaunchID(env)
}

// ciLaunchID picks a stable-per-job id from common CI env vars so two pipeline
// runs that share TRACERA_RUN_TITLE still get separate handoff files.
func ciLaunchID(env map[string]string) string {
	if env == nil {
		return ""
	}
	if run := strings.TrimSpace(env["GITHUB_RUN_ID"]); run != "" {
		attempt := strings.TrimSpace(env["GITHUB_RUN_ATTEMPT"])
		if attempt == "" {
			attempt = "1"
		}
		return "github:" + run + "." + attempt
	}
	if pipeline := strings.TrimSpace(env["CI_PIPELINE_ID"]); pipeline != "" {
		return "gitlab:" + pipeline
	}
	if build := strings.TrimSpace(env["BUILDKITE_BUILD_ID"]); build != "" {
		return "buildkite:" + build
	}
	if build := strings.TrimSpace(env["CIRCLE_BUILD_NUM"]); build != "" {
		return "circle:" + build
	}
	if build := strings.TrimSpace(env["BUILD_BUILDID"]); build != "" {
		return "azdo:" + build
	}
	if build := strings.TrimSpace(env["TRAVIS_BUILD_ID"]); build != "" {
		return "travis:" + build
	}
	return ""
}

func handoffPaths(projectID int, runTitle string) (idPath, lockPath string) {
	return handoffPathsFor(projectID, runTitle, EnvMap())
}

// handoffTempDir is where peer handoff files live. Tests override it because
// os.TempDir caches its first result for the process lifetime.
var handoffTempDir = os.TempDir

func handoffPathsFor(projectID int, runTitle string, env map[string]string) (idPath, lockPath string) {
	payload := fmt.Sprintf("%d\x00%s", projectID, runTitle)
	if launch := handoffLaunchKey(env); launch != "" {
		payload += "\x00" + launch
	}
	sum := sha256.Sum256([]byte(payload))
	digest := hex.EncodeToString(sum[:])[:16]
	stem := filepath.Join(handoffTempDir(), "tracera-handoff", fmt.Sprintf("run-%d-%s", projectID, digest))
	return stem + ".id", stem + ".lock"
}

func peerCount(env map[string]string) int {
	if env == nil {
		env = EnvMap()
	}
	raw := strings.TrimSpace(env[PeerCountEnv])
	if raw == "" {
		return 0
	}
	n, ok := parseStrictInt(raw)
	if !ok || n <= 0 {
		return 0
	}
	return n
}

func writeHandoffState(idPath string, runID, refs, claimed int) error {
	if err := os.MkdirAll(filepath.Dir(idPath), 0o755); err != nil {
		return err
	}
	tmp := idPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d\n%d\n%d\n", runID, refs, claimed)), 0o644); err != nil {
		return err
	}
	// Windows rename fails when the destination exists; remove first so
	// refs++ updates work on every OS (Python Path.replace / .NET File.Move overwrite).
	_ = os.Remove(idPath)
	return os.Rename(tmp, idPath)
}

func readHandoffState(idPath string) (runID, refs, claimed int, ok bool) {
	raw, err := os.ReadFile(idPath)
	if err != nil {
		return 0, 0, 0, false
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 {
		return 0, 0, 0, false
	}
	runID, err = strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || runID <= 0 {
		return 0, 0, 0, false
	}
	refs = 1
	if len(lines) > 1 {
		refs, err = strconv.Atoi(strings.TrimSpace(lines[1]))
		if err != nil || refs < 0 {
			return 0, 0, 0, false
		}
	}
	// Legacy two-line files: treat claimed as current refs (best effort).
	claimed = refs
	if claimed < 1 {
		claimed = 1
	}
	if len(lines) > 2 {
		claimed, err = strconv.Atoi(strings.TrimSpace(lines[2]))
		if err != nil || claimed < 0 {
			return 0, 0, 0, false
		}
	}
	return runID, refs, claimed, true
}

// handoffLockStaleAfter is how old a lock file may get before it is treated as
// left behind by a crashed process. A holder keeps the lock for one create-run
// request (HTTPTimeout) at most, so anything older than this is dead.
const handoffLockStaleAfter = 3 * HTTPTimeout

// handoffIDStaleAfter is how long a .id file may sit on disk before the next
// peer claim treats it as an abandoned run and creates a fresh one (crash,
// killed CI job, or a suite that never called ReleasePeerRun). Long enough for
// overnight suites; stale files must not attach to a finished run. Same 24h
// TTL as JS / Python / JVM / .NET handoff.
const handoffIDStaleAfter = 24 * time.Hour

func handoffIDStale(idPath string) bool {
	info, err := os.Stat(idPath)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) >= handoffIDStaleAfter
}

// reclaimStaleLock removes a lock file no live holder can still own.
func reclaimStaleLock(lockPath string) bool {
	info, err := os.Stat(lockPath)
	if err != nil || time.Since(info.ModTime()) < handoffLockStaleAfter {
		return false
	}
	return os.Remove(lockPath) == nil
}

// acquireLock takes an exclusive O_EXCL lock file; portable on every OS.
func acquireLock(lockPath string, timeout time.Duration) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		fh, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			return fh, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if reclaimStaleLock(lockPath) {
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("Tracera: timed out waiting for handoff lock %s", lockPath)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func releaseLock(lockPath string, fh *os.File) {
	_ = fh.Close()
	_ = os.Remove(lockPath)
}

// ClaimPeerRun attaches to the shared run id, creating it on first claim.
// The second return value is true for the process that created the run.
func ClaimPeerRun(projectID int, runTitle string, createRun func() (int, error)) (int, bool, error) {
	idPath, lockPath := handoffPaths(projectID, runTitle)
	fh, err := acquireLock(lockPath, 60*time.Second)
	if err != nil {
		return 0, false, err
	}
	defer releaseLock(lockPath, fh)

	if runID, refs, claimed, ok := readHandoffState(idPath); ok {
		if handoffIDStale(idPath) {
			_ = os.Remove(idPath)
			LogDebug("Tracera: peer handoff .id file stale (>%s), creating a new run", handoffIDStaleAfter)
		} else {
			if err := writeHandoffState(idPath, runID, refs+1, claimed+1); err != nil {
				return 0, false, err
			}
			LogDebug("Tracera: peer handoff attach run #%d (refs=%d, claimed=%d)", runID, refs+1, claimed+1)
			return runID, false, nil
		}
	}
	runID, err := createRun()
	if err != nil {
		return 0, false, err
	}
	if err := writeHandoffState(idPath, runID, 1, 1); err != nil {
		return 0, false, err
	}
	LogDebug("Tracera: peer handoff created run #%d", runID)
	return runID, true, nil
}

// DiscardPeerRun drops the shared handoff state without finishing the run.
// The process that owns the finish calls it so the next suite run starts from
// a clean slate instead of attaching to the run it just closed.
func DiscardPeerRun(projectID int, runTitle string) {
	idPath, lockPath := handoffPaths(projectID, runTitle)
	if _, err := os.Stat(idPath); err != nil {
		return
	}
	fh, err := acquireLock(lockPath, 60*time.Second)
	if err != nil {
		LogDebug("Tracera: %s", err)
		return
	}
	defer releaseLock(lockPath, fh)
	_ = os.Remove(idPath)
}

// ReleasePeerRun drops one participant; the last expected peer calls finish.
func ReleasePeerRun(projectID int, runTitle string, finish func()) {
	idPath, lockPath := handoffPaths(projectID, runTitle)
	if _, err := os.Stat(idPath); err != nil {
		return
	}
	fh, err := acquireLock(lockPath, 60*time.Second)
	if err != nil {
		LogDebug("Tracera: %s", err)
		return
	}
	defer releaseLock(lockPath, fh)

	runID, refs, claimed, ok := readHandoffState(idPath)
	if !ok {
		return
	}
	refs--
	if refs < 0 {
		refs = 0
	}
	if refs > 0 {
		_ = writeHandoffState(idPath, runID, refs, claimed)
		LogDebug("Tracera: peer handoff release run #%d (refs=%d)", runID, refs)
		return
	}
	expected := peerCount(EnvMap())
	if expected > 0 && claimed < expected {
		_ = writeHandoffState(idPath, runID, refs, claimed)
		LogDebug(
			"Tracera: peer handoff holding run #%d open (claimed=%d/%d, waiting for peers)",
			runID, claimed, expected,
		)
		return
	}
	finish()
	_ = os.Remove(idPath)
	LogDebug("Tracera: peer handoff finished run #%d", runID)
}
