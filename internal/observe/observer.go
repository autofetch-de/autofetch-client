package observe

import "time"

// Observer receives best-effort lifecycle updates from the client runtime.
// Implementations must be concurrency-safe.
type Observer interface {
	Poll(ok bool, at time.Time, err error)
	JobLeased(jobID, jobType string)
	JobCleared()
	DownloadStarted(target string)
	DownloadProgress(target string, downloaded, total int64, speedBps float64, eta time.Duration)
	DownloadFinished(target, status string)
	Error(err error)
}

type NopObserver struct{}

func (NopObserver) Poll(bool, time.Time, error)                                   {}
func (NopObserver) JobLeased(string, string)                                      {}
func (NopObserver) JobCleared()                                                   {}
func (NopObserver) DownloadStarted(string)                                        {}
func (NopObserver) DownloadProgress(string, int64, int64, float64, time.Duration) {}
func (NopObserver) DownloadFinished(string, string)                               {}
func (NopObserver) Error(error)                                                   {}
