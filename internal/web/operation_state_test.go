package web

import "testing"

func TestJobStateSettle(t *testing.T) {
	for _, c := range []struct {
		from  jobState
		ended bool
		code  string
		want  jobState
	}{
		{jobStarting, false, "", jobFailed},
		{jobDownloading, false, "too_large", jobFailed},
		{jobDownloading, true, "", jobCancelled},
		{jobDownloading, true, "too_large", jobFailed}, // the code explains it
		{jobDone, true, "", jobDone},
		{jobChanged, true, "", jobChanged},
	} {
		if got := c.from.settle(c.ended, c.code); got != c.want {
			t.Errorf("%s.settle(%v, %q) = %s, want %s", c.from, c.ended, c.code, got, c.want)
		}
	}
	if got := jobDone.to(jobDownloading); got != jobDone {
		t.Errorf("a finished download moved to %s", got)
	}
}

// The helper can end (failing the upload) while the browser's last part is
// still being handled; finishing must not hide that result.
func TestUploadFinishingKeepsAFinishedResult(t *testing.T) {
	for _, s := range []uploadState{uploadDone, uploadFailed, uploadCancelled} {
		if got := s.to(uploadFinishing); got != s {
			t.Errorf("%s.to(finishing) = %s", s, got)
		}
	}
	if got := uploadReceiving.to(uploadFinishing); got != uploadFinishing {
		t.Errorf("receiving.to(finishing) = %s", got)
	}
}
