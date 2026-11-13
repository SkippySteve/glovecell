package web

// The console's download checks, downloads and uploads each move through a
// small set of states. The browser reads them as the "state" string in JSON
// (web/src), so the values must not change.

// checkState is where a redirect-chain check stands: checking, then ready
// or failed. A ready check is deleted when a download uses it.
type checkState string

const (
	checkChecking checkState = "checking"
	checkReady    checkState = "ready"
	checkFailed   checkState = "failed"
)

// jobState is where a download stands: starting, then downloading (once per
// hop), then one finished state.
type jobState string

const (
	jobStarting    jobState = "starting"
	jobDownloading jobState = "downloading"
	jobDone        jobState = "done"
	jobFailed      jobState = "failed"
	jobChanged     jobState = "changed"   // the redirect chain differs from the check
	jobCancelled   jobState = "cancelled" // cancelled or timed out
)

func (s jobState) finished() bool {
	return s == jobDone || s == jobFailed || s == jobChanged || s == jobCancelled
}

// to returns next, or s when s is already finished: a finished download
// never changes state again.
func (s jobState) to(next jobState) jobState {
	if s.finished() {
		return s
	}
	return next
}

// settle is the state a download ends in. One that stopped before finishing
// failed, or was cancelled when its context ended and nothing more specific
// (a code) explains it.
func (s jobState) settle(contextEnded bool, code string) jobState {
	switch {
	case s.finished():
		return s
	case contextEnded && code == "":
		return jobCancelled
	default:
		return jobFailed
	}
}

// uploadState is where an upload stands: starting, receiving (parts
// arrive), finishing (the last part arrived; the helper is checking the
// file), then one finished state.
type uploadState string

const (
	uploadStarting  uploadState = "starting"
	uploadReceiving uploadState = "receiving"
	uploadFinishing uploadState = "finishing"
	uploadDone      uploadState = "done"
	uploadFailed    uploadState = "failed"
	uploadCancelled uploadState = "cancelled"
)

func (s uploadState) finished() bool {
	return s == uploadDone || s == uploadFailed || s == uploadCancelled
}

// to returns next, or s when s is already finished. The helper can end (and
// the upload finish) while the last part is still being handled; finishing
// must not overwrite that result.
func (s uploadState) to(next uploadState) uploadState {
	if s.finished() {
		return s
	}
	return next
}
