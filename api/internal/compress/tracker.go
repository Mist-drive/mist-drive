package compress

// ProcessingTracker allows the compress engine to mark files as in-progress
// so the file listing API can reflect that state to clients.
// *httpx.Server satisfies this interface via its exported AddProcessing / RemoveProcessing methods.
type ProcessingTracker interface {
	AddProcessing(userID, key string)
	RemoveProcessing(userID, key string)
}

// QuotaUpdater allows the compress engine to adjust a user's usedBytes after
// replacing a file with a smaller recompressed version.
// *users.Store satisfies this interface via its AddUsedBytes method.
type QuotaUpdater interface {
	AddUsedBytes(userID string, delta int64) error
}
